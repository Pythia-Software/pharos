> Working notes behind [the test suite survey](../testing-survey.md), written by one of the five 2026-10-19 reviews at `bb496be`. Line numbers refer to that commit. Classifications are judgement calls; the timings here are indicative, taken from contended runs or the Pharos archive. See the survey for measured numbers. The `tests/*-ui.test.cjs` browser files reviewed here have since been deleted; the browser tier is now the structural suite in `web/tests/browser/`.

# Browser test audit: Home / query-table cluster

Scope: `tests/home-ui`, `library-query-ui`, `usage-ui`, `usage-presets-ui`, `usage-table-colors-ui`, `tools-ui`, `writing-messages-ui`, `select-ui`, `combo-button-ui`, `search-editor-ui` (`*.test.cjs`). That is 70 test cases, 66 of which launch Chrome.

Method: I read every test and checked it against `web/src` and against the node tests in `web/tests/*.mjs` and `tests/chart-*.test.cjs` / `usage-chart-colors` / `authored-categories`. I used `git log` to see which PR added each test. I ran no tests. The failure and duration data comes from agent runs recorded in the Pharos archive: the Query Table 0.8.0 baseline run on 2026-10-18, and runs on 10-13, 10-14 and 10-15.

---

## 0. Headline numbers

| | |
|---|---|
| Test cases in scope | **70** (home-ui 45, search-editor 8, combo-button 6, library-query 4, usage-table-colors 2, the other five files 1 each) |
| Chrome launches per run of these files | **≈67**: every test launches its own browser. home-ui alone launches 42 because `setup()` calls `chromium.launch`, and the carousel test calls `setup()` twice. |
| Screenshots taken | **42**: 34 in home-ui, of which 9 are `fullPage` at 2133 px / DPR 1.8. **None is ever compared.** Only `combo-button-ui:189` puts its screenshot behind an env var. |
| Fixed sleeps (`waitForTimeout`) | 14 call sites in home-ui. One of them sits in a loop that can add up to 54 × 400 ms (`home-ui:580`). |
| Currently failing or flaky | 3 of 4 tests in `library-query-ui`, `home-ui:1027`, and `usage-table-colors-ui:13` (flaky). `writing-messages-ui` was failing and was probably fixed by #140. Details in §4. |
| Tests in browser files that never use a browser | 4 (`home-ui:129, 148, 346, 373`) |
| Observed durations (contended runs) | home-ui tests take 2 to 20 s each: the belt and carousel tests 18 to 20 s; failures 38 s. The single `usage-ui` and `usage-table-colors` tests took 86 s each. tools-ui and usage-presets took 43 to 44 s each. The whole `tests/*.test.cjs` suite took ≈5.5 min for 254 tests. |
| Why home-ui is the long pole | `node --test` runs files in parallel but runs the tests inside a file one after another. home-ui's 41 browser tests (≈6 to 8 min under contention) therefore set the wall time of the whole `*-ui` run. |

**Dominant classification across the 70 cases:**

| Class | # | % |
|---|---|---|
| BEHAVIOR | 26 | 37% |
| DUPLICATE | 19 | 27% |
| LOGIC-IN-BROWSER | 11 | 16% |
| CHANGE-DETECTOR | 9 | 13% |
| INTEGRATION-ESSENTIAL | 5 | 7% |
| PERF/BENCH | 0 as dominant class | 0% |

PERF/BENCH is 0 because no test is mainly a benchmark. Timing assertions and huge fixtures do appear inside other tests (`home-ui:656`, `1620`, `1081`).

Many tests counted as BEHAVIOR also contain CHANGE-DETECTOR assertions: exact px or colour values, copy text, and screenshots. About 40% of all assertion lines in home-ui are style, copy or pixel checks.

---

## 1. Per-file summary

| File | #tests | Cost drivers | Dominant class(es) | Recommendation | Confidence |
|---|---|---|---|---|---|
| `tests/home-ui.test.cjs` (1783 lines) | 45 (41 browser) | 42 Chrome launches, each loading the full 346 KB `ui.py` page plus the 1.7 MB bundle. Every Home test waits for the whole discovery belt to finish loading. 16 tests open `/home?cards=library`, which renders 80 active card previews. 34 screenshots. 14 fixed sleeps. Repeated `setViewportSize` sweeps of 2 to 4 widths. `1081` collects all 80 cards one at a time and then reloads. | DUPLICATE 15, BEHAVIOR 14, CHANGE-DETECTOR 6, LOGIC 5, INTEGRATION 5 | Move 4 node-only tests to `web/tests`. Delete or merge ≈17 tests that duplicate `card-parity`, `card-loading`, `query-previews`, `card-presentation`, `scatter-axis` or `card-recipes`. Pull the belt geometry and wrap logic out of `query-conveyor.tsx` into a pure function. Fold all the "fits inside cover / no overflow" checks into one layout sweep. Fix the stale assertion at `:1031`. Use one shared browser. Target: about 17 browser tests, down from 41. | High |
| `tests/library-query-ui.test.cjs` | 4 | 4 launches. Two tests always time out at 30 s. | DUPLICATE, CHANGE-DETECTOR, LOGIC (3 of 4 broken) | Delete `:12` (it is a subset of `feedback-ui:156`). Delete `:224`: its UI (`.qt-work-why`, `.why-chips`, `.semantic-hint`) no longer exists anywhere in `web/src` or the assets, and the server side is covered by `internal/archive/search_explain_test.go`. In `:50`, drop the radius and gap asserts and keep only the header/`main` scroll invariant (move it to the layout sweep). Its XML-chip and Markdown checks duplicate `transcript-ui`. Rewrite `:165` as a node test of schema enum labels. | High |
| `tests/usage-ui.test.cjs` | 1 (450 lines) | One monolithic test with about 10 reloads and about 200 steps; observed at 86 s. Stubs only the legacy `/aggregations` endpoint and plain POST, with no `/capabilities`, so it runs the non-production transport. | LOGIC-IN-BROWSER / DUPLICATE | Most of it re-checks `tests/chart-scale`, `chart-period`, `chart-rolling`, `chart-selection` and `chart-colors` through the DOM. Cut it to a wiring test of about 60 lines. See §2. | Medium-high |
| `tests/usage-presets-ui.test.cjs` | 1 | 1 launch, 4 reloads. Its in-route copy of the query-v3 engine duplicates home-ui. | LOGIC-IN-BROWSER | Export `presetMatches` and the preset lists from `index.tsx:886-930,1159` into a module and test them in node. The grouping half already duplicates `web/tests/usage-queries.test.mjs:34-95`. Keep a short click → highlight → reload → Records drill-in smoke. The docs name this as an 0.8.0 validation fixture. | High |
| `tests/usage-table-colors-ui.test.cjs` | 2 | 2 launches. `:13` is flaky: a 30 s timeout on the cell context menu at `:101`, seen on 10-13, 10-14 and the 10-18 baseline, with runs of 36 to 86 s. | DUPLICATE | Delete both. `tests/usage-chart-colors.test.cjs` (7 tests) already covers facet colours, folded Other, model variants, combined providers, model-effort pairs and renderer pass-through. If a wiring guard is wanted, add one assertion (badge colour equals swatch colour) to the slimmed usage-ui test. | High |
| `tests/tools-ui.test.cjs` | 1 | 1 launch. Unknown APIs return `{}`. No `pageerror` check. No `/capabilities` stub, so it uses the legacy transport. | BEHAVIOR | Keep. It is the only guard for undo/redo surviving table pause, which the docs cite. Add an error check. Move into the shared harness. | High |
| `tests/writing-messages-ui.test.cjs` | 1 | 1 launch. Uses the legacy transport. | BEHAVIOR | Keep. Trim the set-chip assertions at `:76-82`, which duplicate `tests/authored-categories.test.cjs`. | Medium |
| `tests/select-ui.test.cjs` | 1 | 1 launch with `setContent`, so cheap. Checks 13 selects × 6 theme combinations. | CHANGE-DETECTOR | Keep only the invariant: one caret per select, meaning a background image implies `appearance:none` plus `no-repeat`, and a sibling SVG caret implies no background image. Drop the exact px, calc and hex values. Run it in the shared browser. | Medium |
| `tests/combo-button-ui.test.cjs` | 6 | 6 launches. `:349` waits for real 900 ms `setTimeout`s in the stub. The file name is misleading: 4 of the 6 tests are about the drive/activity panel. | BEHAVIOR 3, LOGIC 2, CHANGE-DETECTOR 1 | Delete `:99` (pure radius, colour and px checks; its structure is already asserted by `incremental-sync-ui:181,504`). Remove the radius and accent asserts from `:12`. Move the estimate, label and grouping formatting in `:147` and `:299` into a module pulled out of `library.js`, and test it in node. | Medium-high |
| `tests/search-editor-ui.test.cjs` | 8 | 8 launches. Uses a component-level fixture without `ui.py`, which is the right pattern. | BEHAVIOR 6, DUPLICATE 1, LOGIC 1 | Keep most of it, in one shared browser. Drop the CSS-duration and copy asserts. Merge `:143` into `:204`. Tighten the weak assertions at `:300` and `:382`. | High |

---

## 2. Per-test actions: delete, move down, or merge

Class abbreviations: B = BEHAVIOR, L = LOGIC-IN-BROWSER, CD = CHANGE-DETECTOR, D = DUPLICATE, I = INTEGRATION-ESSENTIAL.

### 2a. Already browser-free: move to `web/tests`

| Test | Class | Why | Coverage after the move |
|---|---|---|---|
| `home-ui:129` rolling URLs retain the same query… | D (node-only) | Duplicates `card-recipes.test.mjs:129` (relative URLs across the clock and DST) and `:35` (every destination compiles via `toServerQuery`), plus `query-host-integration.test.mjs:66`. | Move only the `queryFromURL('/usage?…')` and `'/tools?tools_view=skills…'` dataset-routing asserts to `query-host-integration.test.mjs`. |
| `home-ui:148` rolling windows recognize whole local calendar spans… | L (node-only, misplaced) | Unique DST and partial-day cases for `rollingQueryMatches`. | Move as-is to `web/tests/query-host-integration.test.mjs`, running under `TZ=America/Denver`. |
| `home-ui:346` bookmark migration preserves separately named saves… | L (node-only, misplaced) | Unique: two named saves of the same query plus the default ID. `card-recipes:221` covers only rolling migration. | Move to `card-recipes.test.mjs`, which already has a `bookmarkFixture`. |
| `home-ui:373` changing a saved range preserves query structure… | L (node-only, misplaced) | `updateQueryWindowURL` keeping `search`, `case`, `fuzzy`, `heat_grain`, the hash, `any` terms and resetting offset. It partly overlaps `query-host-integration:66` and `card-recipes:179`. | Move to `query-host-integration.test.mjs`. |

These four currently run only when Playwright is installed and esbuild has bundled `query-table-core` at the top of home-ui.

### 2b. Delete: covered by node tests or by another browser test

| Test | Class | Why delete | What still covers it |
|---|---|---|---|
| `home-ui:514` card editor saves extended and all-time windows | D | The same editor flow as `:461`, run with values 180, 365 and all. | `card-recipes:179` (180, 365, all; clears bounds; keeps search and hash). The editor wiring stays in `:461`. |
| `home-ui:827` wide cards inset translucent collection actions… | CD | Exact `7px` radius, 34 px FAB, `0.25` alpha, 4 grid columns, equal padding. | The linear `[10,100]` heights belong in a node test of `column-marks`/`card-presentation`. The card-library overflow check moves to the layout sweep. Keyboard Collect is already exercised in `:1027`. |
| `home-ui:929` native covers retain distributions, paired axes… | D | Cover data duplicates `query-previews:81` ("cover changes reuse full results and preserve distributions and paired axes"). Cover-metric persistence duplicates `:996`. | `query-previews:81,95`, `card-parity` customized covers, and `home-ui:996`. Move the "fits inside cover" bounds into the layout sweep. Keep the "Try grouped comparison" legacy path only if it is the sole guard; otherwise add 3 lines to `:996`. |
| `home-ui:1081` every active catalog card preserves its displayed data… | D (huge fixture) | 80 sequential Collect clicks, each followed by `assertEventually`, then a dashboard of 80 cards. Probably the costliest test in scope. | `card-parity.test.mjs` runs 132 recipes × 4 windows plus every cover kind with metadata edits, comparing catalog and dashboard previews and loading shapes in node. To cover DOM-level equality, add a node `react-dom/server` render of `QueryCard` with catalog props versus dashboard props for each recipe (milliseconds). |
| `home-ui:1111` template edits preserve distribution layout… | D | Saved cover deep-equals the recipe cover after a title edit. | `card-parity` "every active default cover can be saved without changing its settings". The form round-trip is `:1719`; merge the two IDs into it. |
| `home-ui:1129` retained distribution cards preserve native boxes and local-hour queries… | D | Request shape (box distribution, `conversation_id` key) and saved `dateGrouping.timeZone`. | `query-previews:95,119` and `card-recipes:146,355`. |
| `home-ui:1165` collected writing-hours cards combine multiple days by local hour… | D | Hour bucket sums after collect, reload and a window edit. | `query-previews:119` (saved local-hour cards keep 24 combined clock-hour buckets), `human-study:109`, `card-recipes:355`. The axis labels `['00:00','12:00','23:00']` are asserted again in `:1208`. |
| `home-ui:1280` metric editor saves and clears underline label limits… | CD | Exercises Query Table's own metric editor (`List bar placement`, `Maximum label lines`) and `-webkit-line-clamp` values. This is the library's responsibility. | Query Table's own tests. If needed, a node `encodeQuery`/`decodeQuery` round trip of `display.list.maxLabelLines`. |
| `home-ui:1470` unavailable library previews reload and cancel retries… | D | A stale retry must not replace the new window. Uses a `waitForTimeout(100)` negative check. | `query-previews:312,323` (cancellation suppresses publication and cached publication). `use-preview-reload.ts` is a small hook; add a node test with fake setters if wanted. |
| `home-ui:1500` card loading has a spinner and skeleton… | CD | 31 bars, `animationName === 'query-preview-spin'`, `'none'` under reduced motion, `data-loading-kind`, and a screenshot. | `card-loading.test.mjs:44` ("daily covers reserve their headline and calendar bars"). Reduced motion can be one assertion in the shared CSS check (§3). |
| `home-ui:1597` contextual card loaders match saved chart types… | CD | Animation durations `1.5s`/`0.8s`, 100 scatter dots, a dot `transform` that must change after a 300 ms sleep (a timing assertion), 6 whiskers, 6 rows, 7 screenshots. | `card-loading.test.mjs:11,23,32` cover kind and count for every display. If the element-count mapping matters, render `card-loading.tsx` with `react-dom/server` in node. |
| `home-ui:1669` kept cards with a stale cover… can be renamed | D | Regression for renaming a card whose cover is stale. | `card-recipes:391` ("replacing a kept query clears obsolete cover IDs and permits renaming"). |
| `home-ui:1693` card covers keep friendly work labels across twenty visible rows | D | Friendly titles past 12 groups, plus an exact `2M$` Other row. | `query-previews:393` ("labelled covers fetch friendly titles beyond twelve visible groups"). |
| `library-query-ui:12` annotation controls stay above a native modal… | D | A strict subset of `feedback-ui:156`, which does the same modal flow plus dragging. | `feedback-ui:156` |
| `library-query-ui:224` Library search asks for substring matches and explanations… | L (broken, dead UI) | The why-chip UI no longer exists, so this always fails with a 30 s timeout at `:258`. The annotation-preserves-panel part duplicates `feedback-ui`. | Server: `internal/archive/search_explain_test.go`. Request flags `substring`/`explain`: add to `web/tests/search-composer.test.mjs` if the frontend still sends them. |
| `usage-table-colors-ui:13` Usage table badges follow the active graph… | D (flaky) | Times out on `.qt-cell-menu` at `:101`; 36 to 86 s when it fails. | `tests/usage-chart-colors.test.cjs:14,23,40,49,54,63`. Optionally add a one-line badge-equals-swatch check in the usage wiring test. |
| `usage-table-colors-ui:131` Model + effort chart separates levels… | D | | `usage-chart-colors:29`, `web/tests/usage-aggregations.test.mjs:53`, `tests/chart-selection.test.cjs:15`. |
| `combo-button-ui:99` header joins the drive indicator and Index changes… | CD | Only radii, heights, a 1 px overlap and the hover brass colour. | Structure: `incremental-sync-ui:181,504`. |

### 2c. Move the logic down to node, then delete or shrink the browser test

| Test | Class | Node target |
|---|---|---|
| `home-ui:573` loading belt keeps its fixed middle while batches grow in pairs | L | Pull lines 87-113 (publish order) and 135-150 (slots, capacity, overscan, wrap) of `query-conveyor.tsx` into pure `nextPublished(published, pending, loading)` and `beltSlots({shown, center, movement, layout, populating})`. Test pairs-only growth, the fixed seed and odd capacity in node. Drop the browser test, including its 400 ms × N sleeps and centre-pixel checks. |
| `home-ui:644` conveyor slides both ways and wraps… | CD (mixed) | Wrap and next/prev go to the same `beltSlots`/`wrap` node test. Keep the a11y invariants (x-ray cards have tabIndex −1, are aria-hidden unless revealable, and have opacity 0 titles) by merging them into `:615`. Drop the mid-slide pixel check after 180 ms (a timing assertion), the hard-coded x ranges, and 2 full-page screenshots. |
| `home-ui:726` compact daily charts keep visible marks in 90-day windows… | L | 91 columns for a −90d window: a node test on `cardLoadingPresentation` or the column builder. Four distinct category colours: a node test on the category → colour-token map. Marks still visible at 390/320 px goes to the layout sweep. |
| `home-ui:872` annotated covers show scalar values, clean rankings… | D (mixed) | K/M units, the single-group-becomes-scalar rule, no population caption, and the timeline headline unit are all in `card-presentation.test.mjs:11,48,81`. Keep only "labels and ticks fit inside the cover" in the layout sweep. |
| `home-ui:1525` compact log axes use round ticks… | D (mixed) | Tick sets `['100K','10M','1B']`/`['0.001','0.1','10','1K']` are in `scatter-axis.test.mjs:7,53` and `card-presentation:35,60`. Keep the gutter < 35 px and "ticks inside the SVG" checks in the layout sweep. Drop the 2 screenshots per width. |
| `home-ui:1749` retired Steering category stays out of discovery… | L | Retired recipe IDs absent from `cardTemplates()`/discovery, and `categoryForQuery` returning steering for old cards: node, `card-recipes`. The "old saved card still renders and opens" check is 3 lines that can join `:421`. |
| `home-ui:165` Home shows the Dashboard Catalog and retired study URLs open Home | CD | Drop the exact copy (`'Collect cards that help you…'`), 2 screenshots and the vacuous `.home-card-library-nav` check (§4). The 10 `page.goto` calls for retired `?cards=*-review` URLs can become a node test of the route predicate (`home.tsx:20 isDraftReview`). Note `coverage-review` is now a live route. Keep 1 browser sample at most. |
| `home-ui:394` homepage search exposes the full options and opens a clean Library route | D | Option checkboxes → draft duplicates `search-editor-ui:204,310` and `text-search-model:115`. Keep only the Home → `/library` navigation without stale `kind`/`search` params, as 5 lines inside `:219` or `:262`. |
| `home-ui:309` query edits and saving after local midnight preserve a rolling window | D (partial) | `search-composer.test.mjs:55,64` cover the rolling window travelling with the query and disappearing after a manual date edit. Keep the Tools save-after-midnight bookmark flow as one parameter of `:461`, or keep the test and drop the four separate `window_*` param asserts. |
| `usage-ui:19` (the whole file) | L/D | Remove the checks node already covers: scale modes (`chart-scale`), custom hours/days and persistence (`chart-period`), rolling averages (`chart-rolling`), only/add/except legend semantics (`chart-selection`), stable colours and provider grouping across Other expansion (`chart-colors:65,78,86`), share sums. Keep ≈60 lines of wiring: (1) chart controls persist across reload; (2) a legend chip writes `q_usage` and the chart request `where`; (3) menu keyboard navigation (ArrowDown, End, Escape, Tab); (4) the writing toggle and the series request carrying table filters; (5) Settings cards route to Usage; (6) the pricing notice appears and is acknowledged. Drop the colour/fontWeight equality and the screenshot. Stub `/capabilities` v3 so it uses the production transport. |
| `usage-presets-ui:13` | L | Move `presetMatches` (sort `nulls` default, metric IDs and labels ignored, chart subset) and the writing/message/chart preset lists into `web/src/usage-presets.ts`; node-test them next to `usage-queries.test.mjs`. Keep a slim browser smoke: click → highlight → reload, then Records drill-in (`:157-177`), plus the `populationRequests === 0` contract. Drop the exact cell text `['example/repo','$3.00','300','30','1','90.0%']`, which duplicates `usage-queries:22`. |
| `library-query-ui:165` Library filter chips and picker show static option labels… | L (broken) | Fails with a 30 s timeout at `:200` because 0.8.0 moved enums into set editors and collapsed the editor. "Labels shown, raw values in queries" is a schema property: node-test that the `source_kind` enum labels in `schemas/library.schema.json` map `chatgpt → ChatGPT` and `tl1-export → TL1 export`, and that QT's static-option renderer uses them. "Query failed" versus "No work matches" is Query Table behaviour. Delete the browser test. |
| `library-query-ui:50` Library table scrolls with the page below the header… | CD (broken) | Fails at `:107` (`gap` 388 ≠ 0); wrap overflow and radius are also asserted. Keep the real invariant from #43 ("header never scrolls; `main` is the scroll container; `thead` sticks at the top of `main`") in the layout sweep. XML-chip and Markdown rendering in cards duplicates `transcript-ui:674,1117`. Keep 3 lines for "conversation card facts and actions", or delete. |
| `combo-button-ui:147` Running now explains stage progress, ranges… | L | `activityLine`, `activitySummary` and the estimate wording in `internal/archive/assets/library.js` ("~3–5 min left", "1.0 MB/sec", "Measuring pace…", "Waiting for progress…") are pure functions of the activity JSON. Export them (for example `library-format.js` with a UMD/CJS shim) and test the state × fields table in node. Keep one browser check that the panel renders a progressbar. |
| `combo-button-ui:299` standalone maintenance stays separate… | L | Same: `libraryUpdate(activities)` grouping goes to node. |

### 2d. Merge (keep the coverage, fewer launches and page loads)

- **Template editor (`home-ui`):** merge `:1330` (key, series, Other), `:1719` (opening and immediately saving keeps all settings, a real form-initialisation race), `:1111` and the Other-row assertion from `:1693` into one test on `?cards=library&card_search=cost-repos`.
- **Preview failure and refresh:** `:1378` and `:1422` use the same setup. Run them as subtests of one page, or keep both in the shared browser. Replace `waitForTimeout(500)` × 3 (`:1451,1458,1463`) with an explicit "settled" signal (§4).
- **Card editor windows:** `:461` absorbs `:514` (values 180, 365, all as a small loop) and optionally `:309`.
- **Cover selection and drill-in:** `:996` absorbs what is unique in `:929`.
- **Layout sweep (INTEGRATION-ESSENTIAL):** a single test that loads one card-library fixture and one Home fixture, then for widths [1470, 390, 320] asserts:
  - no horizontal overflow on `main`, the document or `.card-library` (now checked in more than 15 places);
  - collection rows centred (`:755`);
  - box labels clear their glyphs and axis ticks stay inside the cover (`:1208`);
  - underline bars sit below wrapped labels (`:1253`);
  - configured list and box rows fit an expanded cover (`:1567`);
  - log-axis gutter and ticks stay inside the SVG (`:1525`);
  - compact marks stay visible at 320 px (`:726`);
  - the Library header and `main` scroll invariant (`library-query-ui:50`).

  This replaces about 9 tests and keeps every real-layout guard. Drop the exact per-width row-count arrays (`[2,1]`, `[1,1,1]`) in favour of "every row centred, no overflow", and replace the 200 to 320 ms sleeps with `transitionend` or rAF waits.
- **search-editor-ui:** merge `:143` (mobile: target labels, reduced-motion `0s`, submit) into `:204`. Its submit path is the same, the copy and CSS checks are change-detectors, and the 390 px overflow check moves to the layout sweep.

### 2e. Keep, with lighter edits

These stay as browser tests because each is the only guard on something real.

- `home-ui:186`: hidden Home does not scan on refresh.
- `home-ui:219`: legacy Library URLs, history and back/forward.
- `home-ui:262`: route restoration beats visibility sync.
- `home-ui:421`: collection CRUD, undo and keyboard reorder.
- `home-ui:461`: the card editor.
- `home-ui:549`: autoplay under a fake clock. Use `emulateMedia` instead of a second launch.
- `home-ui:615`: x-ray reveal, focus and a11y.
- `home-ui:693`: the only drag-and-drop test.
- `home-ui:755`: becomes the layout sweep.
- `home-ui:996`: cover selection and drill-in.
- `home-ui:1027`: card library filter, sort and template persistence. Fix `:1031` first.
- `home-ui:1378`, `:1422`: preview failure, retry and refresh contracts.
- `home-ui:1772`: stale machine preferences versus explicit URL scope.
- `tools-ui:12`
- `writing-messages-ui:26`
- `combo-button-ui:12`: drop the style checks.
- `combo-button-ui:244`
- `combo-button-ui:349`: use a clock or an explicit release instead of the 900 ms server `setTimeout`.
- `search-editor-ui:71`: drop the `0.28s` and height ≤ 60 checks.
- `search-editor-ui:99`, `:119`, `:165`
- `search-editor-ui:204`, `:310`: drop the exact format-list check.
- `search-editor-ui:360`
- `select-ui:32`: relaxed to the invariant.

**Net effect for this cluster:** about 66 browser tests become about 30, all run in one Chrome per file. About 11 cases move to node, where they take milliseconds. About 23 are deleted as duplicates or change-detectors. The removed tests include the most expensive ones (`home-ui:1081`, `:1597`, `:573`, `usage-table-colors:13`) and every test that currently fails.

---

## 3. Harness duplication and a shared harness

### What is copy-pasted (this cluster, and across `tests/*-ui.test.cjs`)

| Fragment | Copies |
|---|---|
| Loading `playwright` from a gitignored per-workspace install | 22 files |
| Slicing the `ui.py` HTML out of `r'''…'''` | 20 files. 3 in scope still probe the deleted `src/pharos/ui.py` path first (`library-query-ui:9`, `usage-ui:10`, `combo-button-ui:9`). |
| Hard-coded `/Applications/Google Chrome.app/…` | 23 files. `home-ui` and `search-editor-ui` have no `existsSync` fallback, so they fail on machines without system Chrome. |
| `chromium.launch` inside each test | 39 call sites; ≈67 launches per run in scope |
| Route handler serving `/assets/query-tables.{js,css}` | 15 files. home-ui re-reads 1.7 MB from disk on every request. |
| `pageerror` → `errors[]` → `assert.deepEqual(errors, [])` | 21 files. tools-ui and `library-query-ui:12` omit it. |
| A fake query server (query-v3, metrics, aggregations, distinct, field-stats, capabilities) built on `query-table-core` (`applyQuery`, `prepareGrouping`, `evaluateMetrics`) | `home-ui:72-120`, `usage-presets-ui:28-65`, `web/tests/card-parity.test.mjs:41-70`, `web/tests/query-previews.test.mjs`, and a v2-only variant in `usage-table-colors-ui:35-58,147-162`. |
| `moduleOf(entry)` (esbuild `buildSync` + `new Function`) | home-ui, plus the same pattern in each `web/tests/*.mjs`. |
| Preference and snapshot endpoints (`/api/preferences`, `/api/query-snapshots`) | home-ui only, but needed by any test that saves. |

**Model to copy:** `tests/feedback-ui.test.cjs` already uses `before()` to launch one browser, `browser.newPage()` per test, and `t.after` to close the page and assert no errors. That pattern should become the default.

### Proposed module: `tests/support/app.cjs`

```js
// tests/support/app.cjs
const { launchBrowser, openApp, mountComponent, fakeQueryServer } = require('./support/app.cjs');

// One Chrome per test file (node --test already isolates files in processes).
let browser;
before(async () => { browser = await launchBrowser(); });   // CHROME_PATH env → system Chrome → bundled Chromium
after(() => browser?.close());

test('collection edits persist', async t => {
  const app = await openApp(t, browser, {
    url: '/',                               // '/', '/home?cards=library', '/usage?…'
    viewport: { width: 1180, height: 920 }, // new BrowserContext per test (≈50 ms) → isolated storage/cookies
    timezoneId: 'America/Denver', reducedMotion: false,
    clock: { install: true, now: '2026-10-16T18:00:00Z' },
    preferences: { 'pharos-theme': 'dark' },  // served at /assets/preferences.js and mutated by /api/preferences
    localStorage: {},
    datasets: { tool_calls: rows, usage: rows }, // defaults to the shared 28-row fixture
    transport: 'v3',                         // 'v3' (prod) | 'legacy' ; stubs /capabilities accordingly
    api: { 'GET /api/pricing': () => pricing, 'POST /api/upgrade': req => … }, // explicit extras
    unknownApi: 'fail',                      // record + 404; assert none in t.after unless allowlisted
    ready: 'home',                           // 'home' | 'cardLibrary' | 'table' | selector | fn
  });
  // app.page, app.requests[] ({dataset, kind:'query-v3'|'rows-v2'|'metrics'|'aggregations'|'distinct', body})
  // app.prefs (live server-side store), app.snapshots
  // app.hold(dataset?) / app.release() / app.fail(dataset) / app.pending
  // app.readQuery(dataset) / app.waitForQuery(dataset, pred) / app.eventually(fn)
  // app.settled()  // resolves when no query request is in flight and the page is idle for 2 rAFs
  // app.snapshot(name) // writes only if PHAROS_UI_SCREENSHOTS is set
});
// t.after (installed by openApp): close context; assert app.errors == [] and no unexpected API calls.

// Component fixtures (search-editor style), bundled once per process with cached esbuild output:
const page = await mountComponent(t, browser, { entry: 'web/src/search-composer.tsx', render: 'HomeSearch', props: {…}, css: 'shell' });
```

Internals:

- `shell()`: slice `ui.py` once and memoise it; also expose `styles` and `sprite` for component fixtures (as `select-ui` and `search-editor-ui` do today).
- `assets()`: read `query-tables.{js,css}`, `library.js` and `upgrade.js` once into memory.
- `fakeQueryServer(schemas, datasets)` lives in `web/tests/support/fake-query-server.mjs`. It exports both `routeHandler(route)` for Playwright and `fetch(url, init)` for node tests, replacing the 4 or 5 copies listed above. This also guarantees browser and node tests run the same fake semantics: ISO datetimes for calendar queries and epoch values otherwise (`home-ui:92-95`).
- `page.setDefaultTimeout(5000)`. Everything is local, so the 30 s default only makes broken tests slow: library-query-ui spends 60 s on its two timeouts.
- An explicit `app.settled()` replaces `waitForTimeout(350/500)` used to wait for a preference debounce or for no further requests.

**Expected savings.** Two cost drivers go away:

1. Chrome launch and teardown: ≈0.5 to 1.5 s each, about 67 times.
2. Cold V8 compile of the 1.7 MB bundle in a fresh profile, on every test.

Combined with §2, home-ui drops from 41 to about 17 browser tests in one Chrome. Much less CPU contention also means fewer flakes in the files running in parallel with it. One option is to split home-ui into `home-belt`, `home-collection` and `card-library` files so they run in parallel, each with one Chrome. This only helps on unloaded machines; with several agents per machine, a single file with one browser is the better default.

---

## 4. Surprises

### Failing or broken tests in scope

The docs' list ("Library, MCP, navigator, Findings, Sync, card library, pricing catalog, Usage badge, Human Words") maps to this scope as follows. The failing-test list is from the 2026-10-18 0.8.0 baseline run in the Pharos archive (`not ok 110, 137, 138, 139, 243, 246`).

| Docs name | Test | Failure | Root cause |
|---|---|---|---|
| Library | `library-query-ui:50` | `:107` `388 !== 0` | Layout change-detector (gap between query builder and table). |
| Library | `library-query-ui:165` | 30 s timeout at `:200` | The chip label `Edit source_kind filter value` no longer exists after 0.8.0 set editors and the collapsed editor. |
| Library | `library-query-ui:224` | 30 s timeout at `:258` | Dead UI: `.qt-work-why`, `.why-chips` and `.semantic-hint` appear nowhere in `web/src` or the assets. |
| card library | `home-ui:1027` | `:1031` `1 !== 0` | `a[href*="-review"]` now matches the deliberate `/home?cards=coverage-review` link (`card-library.tsx:81`). Nothing after line 1031 runs, so filter, sort, template persistence and reset have **no running browser coverage today**. |
| Usage badge | `usage-table-colors-ui:13` | Flaky; 30 s timeout on `.qt-cell-menu` at `:101` | Failed on 10-13, 10-14 and the 10-18 baseline; passed on the 0.8.0 branch run. |
| Human Words | `writing-messages-ui:26` | `:77` set chips `[]` | The editor collapsed. #140 (`bb496be`) added the step that expands it (`:77`), so it is **probably passing now**; not verified. |
| pricing catalog | Not in scope | | `tests/chart-colors.test.cjs:24` ("static catalog covers priced models"), a node test. |

MCP, navigator, Findings and Sync are outside this cluster.

### Assertions that can never fail (vacuous)

- `home-ui:171` checks `.home-card-library-nav` count is 0, `home-ui:679` checks `.home-conveyor-foot` count is 0, and `home-ui:853` checks `.query-timeline-scale,.query-cover-columns.broken` count is 0. None of these class names exists anywhere in `web/src`, `query-tables.{js,css}` or `ui.py`. (`.broken` exists only on `.usage-chart-plot`.)
- Negative checks that sleep and then look: `home-ui:1451/1458/1463` sleep 500 ms and then assert `requests == []`, and `home-ui:1492` sleeps 100 ms and then asserts the stale result did not land. If the unwanted rerun arrives later than the sleep, these still pass.
- Weak disjunctions: `search-editor-ui:300` (`second.negated || second.op !== 'starts_with'`) and `:382` (`negated || op !== 'contains'`). These pass whenever the default operator differs, whether or not anything was negated.
- `tools-ui` has no `pageerror` check, and its catch-all stub returns `respond({})` for every unknown path, so API-shape crashes are swallowed.

### Screenshots never compared

All 42 screenshots in scope are write-only artefacts left over from design review: paths like `.context/homepage-design/*`, `.context/card-review/*`, `review2-*`. None is compared. Further:

- `home-ui:693` and `:726` both write `homepage-design/home-implemented{,-narrow}.png`, so each overwrites the other.
- Nine use `fullPage: true` at DPR 1.8 to 2 (for example 3840 × 1935 px PNG encodes).
- `home-ui:1590` writes a debug screenshot only when it fails, which is fine.
- `combo-button-ui:189` already gates its screenshot behind `PHAROS_UI_SCREENSHOTS`; do the same for the rest.

### Fixed sleeps and timing assertions

- Sleeps in home-ui: `:446` 350 ms, `:488` 350 ms, `:577` 20 ms × up to 50, `:580` 400 ms per released batch (up to 54 iterations), `:656` 180 ms, `:720` 250 ms, `:789` 320 ms × 3, `:804` 200 ms, `:814` 200 ms × 3, `:1451/1458/1463` 500 ms each, `:1492` 100 ms, `:1620` 300 ms.
- `library-query-ui:191-195` polls at 50 ms × 50.
- `combo-button-ui:367,384` use real `setTimeout(900)` in the route stub.
- **Timing assertions:**
  - `home-ui:656-658` requires the card to have moved more than 10 px 180 ms into a 2040 ms CSS transition. Under CPU contention this can fail.
  - `home-ui:1620-1621` requires a dot's CSS transform to change within 300 ms.
- The carousel test (`:549`) and `combo-button-ui:12` correctly use Playwright's fake clock.

### Tests that bypass the production query path

`usage-ui`, `usage-table-colors-ui`, `tools-ui`, `writing-messages-ui` and `library-query-ui` do not stub `/api/query/<ds>/capabilities` and `/api/query-computed-columns`; they return 404 or `{}`. Their tables therefore run on the legacy transport (`fetchRows` via POST `/api/query/<ds>`, and `/aggregations`), not the v3 path (`fetchQueryV3`, `index.tsx:238`) that production uses. Only home-ui and usage-presets stub v3. The shared fake server should default to v3. (Medium-high confidence, from reading `index.tsx:230-290,456-472`.)

### Other notes

- Many PRs added browser tests together with node tests for the same logic. `#119` (`95862d4`) alone added 873 lines to home-ui along with `card-loading`, `card-presentation` and `card-recipes` node tests covering the same logic, which is where most of the DUPLICATE class comes from. The tests read like review artefacts: each feature got a node test and a screenshot-taking browser test.
- `node --test tests/*-ui.test.cjs` runs files concurrently. While home-ui runs serially, the other 20-plus files each start their own Chromes, so contention is self-inflicted. This explains the observed single-test durations of 43 to 86 s and the flakiness of `usage-table-colors-ui:13`.
- Several tests mutate `global.location` before entering `try` (for example `home-ui:463`, `:1281`, `:1670`). If `setup()` throws, the global leaks into later tests in the same process.
- `home-ui:549` launches a second browser only to emulate reduced motion; `page.emulateMedia({reducedMotion:'reduce'})` does this on the existing page.

---

## Appendix: classification of all 70 cases

Abbreviations as in §2. In the Node coverage column, "none" means no node test covers the behaviour today. Actions: keep, slim, merge (into the named test), move (to node), fix, delete.

| Test | Class | Node coverage | Action |
|---|---|---|---|
| home-ui:129 rolling URLs… | D | card-recipes:35,129; qhi:66 | move (unique bits) |
| home-ui:148 rolling windows DST… | L | qhi:66 partial | move |
| home-ui:165 Dashboard Catalog / retired URLs | CD | none | slim |
| home-ui:186 nav/logo suspend scans | B | none | keep (1 iteration) |
| home-ui:219 old root Library URLs/history | B | search-composer:41 partial | keep |
| home-ui:262 restored queries per tab | I | none | keep |
| home-ui:309 edits after midnight keep window | D | search-composer:55,64 | merge → :461 |
| home-ui:346 bookmark migration named saves | L | card-recipes:221 partial | move |
| home-ui:373 changing saved range | L | qhi:66, card-recipes:179 partial | move |
| home-ui:394 homepage search → Library | D | search-editor-ui:204,310; text-search-model:115 | merge → :219 |
| home-ui:421 collection CRUD/undo/reorder | B | card-parity collect | keep |
| home-ui:461 editor timescale/preview/nav | B | card-recipes:179,238 | keep, slim |
| home-ui:514 extended/all-time windows | D | card-recipes:179 | delete |
| home-ui:549 carousel autoplay | B | none | keep (emulateMedia) |
| home-ui:573 belt grows in pairs | L | none (extractable) | move |
| home-ui:615 x-ray reveal & focus | B | none | keep (+:644 a11y) |
| home-ui:644 slides/wraps/ghosts | CD | none (extractable) | move/merge |
| home-ui:693 drag saves exact query | B | card-parity collect | keep |
| home-ui:726 90-day marks narrow | L | card-loading:44 partial | move + layout sweep |
| home-ui:755 centering/overflow | I | none | layout sweep |
| home-ui:827 translucent FAB / linear | CD | card-presentation partial | delete |
| home-ui:872 annotated covers | D | card-presentation:11,48,81 | layout sweep only |
| home-ui:929 native covers/dashboards | D | query-previews:81,95 | merge → :996 |
| home-ui:996 grouped cards drill-in | B | card-recipes:91 | keep |
| home-ui:1027 card library filter/sort/templates | B (broken) | none for filter/sort | fix :1031, keep |
| home-ui:1081 every catalog card parity | D | card-parity (all) | delete (+SSR parity) |
| home-ui:1111 template edits keep cover | D | card-parity default covers | merge → :1719 |
| home-ui:1129 distribution/local-hour | D | query-previews:95,119; card-recipes:355 | delete |
| home-ui:1165 writing-hours combine | D | query-previews:119; human-study:109 | delete |
| home-ui:1208 box rows/ticks readable | I | card-presentation:69 partial | layout sweep |
| home-ui:1253 long labels wrap | I | card-presentation:96 | layout sweep |
| home-ui:1280 metric editor label limits | CD | n/a (Query Table library) | delete |
| home-ui:1330 keys/series/Other configurable | B | card-recipes:22; card-parity | merge (template editor) |
| home-ui:1378 unavailable preview retry | B | query-previews:283 partial | keep, slim |
| home-ui:1422 refresh on sync/hidden tab | B | none | keep, replace sleeps |
| home-ui:1470 cancel stale retries | D | query-previews:312,323 | delete |
| home-ui:1500 spinner/skeleton | CD | card-loading:44 | delete |
| home-ui:1525 log axes ticks/gutter | D | scatter-axis:7,53; card-presentation:35,60 | layout sweep only |
| home-ui:1567 rows expand cover | I | none | layout sweep |
| home-ui:1597 contextual loaders | CD | card-loading:11,23,32 | delete |
| home-ui:1669 stale cover rename | D | card-recipes:391 | delete |
| home-ui:1693 friendly labels 20 rows | D | query-previews:393 | delete |
| home-ui:1719 open+save keeps cover | B | card-parity partial | keep (merge target) |
| home-ui:1749 retired Steering | L | card-recipes partial | move + 3 lines → :421 |
| home-ui:1772 stale Machine prefs | B | none | keep |
| library-query-ui:12 annotation above modal | D | feedback-ui:156 | delete |
| library-query-ui:50 table scroll / cards | CD (broken) | transcript-ui:674,1117 | layout sweep only |
| library-query-ui:165 static option labels | L (broken) | none (schema) | move |
| library-query-ui:224 substring/why chips | L (broken, dead UI) | search_explain_test.go | delete |
| usage-ui:19 monolith | L | tests/chart-scale, chart-period, chart-rolling, chart-selection, chart-colors | slim to wiring |
| usage-presets-ui:13 | L | usage-queries:22-95 | move presetMatches + slim |
| usage-table-colors-ui:13 badges | D (flaky) | tests/usage-chart-colors (6) | delete |
| usage-table-colors-ui:131 model+effort | D | usage-chart-colors:29; usage-aggregations:53 | delete |
| tools-ui:12 | B | none | keep |
| writing-messages-ui:26 | B | authored-categories:12,32 partial | keep, slim |
| select-ui:32 carets | CD | none | relax, keep |
| combo-button-ui:12 usage summary auto | B | none | keep, drop style |
| combo-button-ui:99 header combo | CD | incremental-sync-ui:181,504 | delete |
| combo-button-ui:147 Running now estimates | L | none (library.js) | move |
| combo-button-ui:244 Capture & index upgrade | B | none | keep |
| combo-button-ui:299 standalone maintenance | L | none (library.js) | move |
| combo-button-ui:349 update hand-off busy | B | none | keep (clock) |
| search-editor-ui:71 collapsed/animate/drafts | B | none | keep, drop CSS |
| search-editor-ui:99 Rules↔Syntax round trip | B | text-search-model:71 partial | keep |
| search-editor-ui:119 syntax→Rules / invalid | B | none | keep |
| search-editor-ui:143 mobile targets | D | search-editor-ui:204; text-search-model:47 | merge → :204 |
| search-editor-ui:165 wrap/English/Enter | B | text-search-model:82 (reading) | keep, drop reading text |
| search-editor-ui:204 every condition reaches search | B | search-composer:7 (model) | keep (fix weak assertion) |
| search-editor-ui:310 exact/regex/formats/file/url | B | text-search-model:101; search-composer file/url | keep, slim |
| search-editor-ui:360 record search | B | none | keep (fix weak assertion) |

`qhi` = `web/tests/query-host-integration.test.mjs`. Other node test files are in `web/tests/` (`.mjs`) or `tests/` (`chart-*`, `usage-chart-colors`, `authored-categories`).
