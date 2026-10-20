> Working notes behind [the test suite survey](../testing-survey.md), written by one of the five 2026-10-19 reviews at `bb496be`. Line numbers refer to that commit. Classifications are judgement calls; the timings here are indicative, taken from contended runs or the Pharos archive. See the survey for measured numbers.

# Browser page tests: survey (tests/*-ui.test.cjs, page-level subset)

Scope: transcript, delegation, carbon, feedback, findings, machine-activity,
incremental-sync, library-drive, library-link, health, mcp, navigator and tl1
(13 files, **143 test cases**). Fixtures: `tests/transcript-find-fixtures.json`,
`tests/token-usage-fixtures.json`.

Method: I read every test, the code each one exercises (`internal/archive/assets/ui.py`
inline script, `carbon.js`, `sync.js`, `library.js`, `web/src/*.tsx`), the overlapping
node tests (`web/tests/*.mjs`, `tests/*.test.cjs`), the Go tests, and git history. I
ran nothing. Timings and pass/fail status come from the Pharos archive (agent runs on
2026-10-15 and 2026-10-18), not from new runs.

## Headline numbers

| Class | Cases | Share |
|---|---|---|
| BEHAVIOR | 66 | 46% |
| LOGIC-IN-BROWSER | 28 | 20% |
| DUPLICATE | 18 | 13% |
| CHANGE-DETECTOR (dominant) | 16 | 11% |
| INTEGRATION-ESSENTIAL | 12 | 8% |
| PERF/BENCH (huge fixtures; none time anything) | 3 | 2% |

- About a third of the BEHAVIOR and INTEGRATION tests also contain change-detector
  assertions: exact CSS values, pixel deltas, "removed control is still absent", exact
  copy. These assertions are the usual reason the tests break.
- **11 of the 143 cases are broken or flaky on master** (details in §4). Broken
  tests that fail on a locator burn Playwright's 30 s default timeout, because only
  two of the 13 files set `setDefaultTimeout`.
- Measured cost: transcript-ui took **106 s** for 70 tests. The pure-logic test
  `parsed message bodies are reused…` alone took 646 ms, all of it page setup.
  delegation-ui took **57 s** for 20 tests (about 1.4 s minimum per test, mostly
  parsing the 1.7 MB React bundle). library-drive took **163 s**, with 3 failures
  at 31–62 s. library-link took 3.4–4.5 s per matrix case. carbon took about
  14 s. machine-activity took about 25 s.
- The 13 files make **34 Chrome launches** per run: 30 launch per test (carbon,
  findings, machine-activity, incremental-sync, library-link, navigator, health, mcp,
  tl1) and 4 launch once per file. `node --test tests/*-ui.test.cjs` runs files in
  parallel processes, so these Chromes compete for CPU. The same delegation test
  took 4.6 s alone and 13.3 s under contention.
- **26 screenshots are taken unconditionally and never compared.** Nothing reads
  them; they are written into `.context/*.png`.

Rough projection if the recommendations below are applied: in-scope browser time
goes from about 560 s to 130–150 s per full run, a 70–75% cut. That counts serial
test-seconds, including about 150 s of timeouts from broken tests and the
library-drive E2E. It is low-to-medium confidence.

---

## 1. Per-file table

| File | #tests | Cost drivers | Dominant class(es) | Recommendation | Conf. |
|---|---|---|---|---|---|
| transcript-ui.test.cjs | 70 | One browser per file, but a new page plus a full app-HTML load (346 KB `ui.py`) per test, so about 0.65 s minimum and 106 s total. `query-tables.js` is stubbed empty. 6 fixtures have 93–2,202 events. 2 fixed sleeps. 5 different screenshot env vars. Injected CSS hides the whole app shell. | BEHAVIOR 28, LOGIC 21, CD 8, DUP 7, PERF 3, INTEG 3 | Move 21 logic tests to node. This needs the reader model pulled out of `ui.py` (§3). Delete 4 change-detectors (:35, :89, :780, :1105) and 3 feedback duplicates. Make 7 merges (§2c). Reuse one loaded page per file for render-only tests. Expect about 35 browser tests and about 20–25 s. | High (class), Med (time) |
| delegation-ui.test.cjs | 23 | One browser per file. Each test opens a page that loads the real 1.7 MB bundle, read from disk on every request. 9 fixed sleeps (100–150 ms). The fake server re-implements sort, collapse and search. The 390 px no-overflow check is repeated in 4 tests. | BEHAVIOR 14, CD 4, INTEG 4, LOGIC 1 | Keep most. Strip CSS, geometry and "absent control" assertions from 4 tests. Export and unit-test `formatted`, `cellTitle`, `clampRange` and `hierarchyFrames` from `web/src/delegation.tsx`. Replace sleeps with URL waits. Merge 2 pairs. Collapse the mobile checks into one test. | Med-High |
| carbon-ui.test.cjs | 3 (90–300 lines each) | Chrome launched per test. 9 unconditional screenshots at several viewports. Real `carbon.js` plus the React bundle. A 10 ms busy-wait loop. Each test is a long script where the first failure hides the rest. | LOGIC 2, BEHAVIOR 1 | Move about 20 exact CO₂/Wh figures to a node test. `carbon.js` exposes `window.pharosCarbon.{estimate,energy,mass}` and only touches `document.head` and `window.dispatchEvent` at load, so a 10-line vm stub is enough. Delete all screenshots. Split test 3 into focused tests on a shared browser. | High |
| feedback-ui.test.cjs | 11 | One browser per file. `ui.py` only, with all JS stubbed. Real pointer drags. 3 unconditional screenshots. | BEHAVIOR 6, INTEG 3, DUP 2 | Delete :323 (it is a screenshot generator). Merge :88 into :59. Drop the color asserts. Also delete the 3 feedback copies in transcript-ui. | Med-High |
| findings-ui.test.cjs | 4 | Chrome launched per test. React bundle. One unconditional full-page screenshot at 1920 px, which also changes the viewport for later assertions. | CD 2 (both broken), BEHAVIOR 2 | Rewrite :100 to check only routing, badge count and back/forward. Delete the :250 copy assertions or move them to a node test of the sentence builder. Keep :182 and :228. | High |
| machine-activity-ui.test.cjs | 6 | Chrome launched per test (6). React bundle. `query-table-core` serves as the fake backend, which is good. 3 unconditional screenshots. 200 ms scroll-settle debounce ×3. `machine_demo=1` in 3 tests ignores the stubbed rows. | BEHAVIOR 4, DUP 2 | Delete :390 (its logic is already covered by 3 node tests). Fold its reload check into :256. Merge :213 into :256. | Med |
| incremental-sync-ui.test.cjs | 6 | Chrome launched per test. The route table is copy-pasted 6 times. Polling loops with 25 ms sleeps. 5 unconditional screenshots. :12 runs its scenario 3 times. | LOGIC 3, CD 1, BEHAVIOR 2 | :117 and :238 fail every run and :12 is flaky. Move `announceChanges` (sync.js), header busy derivation and run-history formatting to node. Rewrite :117 to check behavior only. Fix :238. | High |
| library-drive-ui.test.cjs | 9 (`describe`/`it`, which is why grep found 0 `test(`) | `go build ./cmd/pharos`, a 3 GB sparse APFS image via `hdiutil`, a real `pharos serve`, and 600 synthetic Claude sessions captured through the CLI. Waits up to 180 s for idle. Tests depend on running in order. macOS only. 163 s with 3 timeouts on 10-15. Picked up by the default `tests/*-ui.test.cjs` glob. | BEHAVIOR 4, INTEG 2, DUP 2, CD 1 | Move it to an opt-in E2E (`PHAROS_E2E=1` or `tests/e2e/`) with only onboarding and cross-Mac indexing, and fix both. Convert the drive panel, refresh, eject and backup-state tests to stubbed harness pages. Delete 3. | High |
| library-link-ui.test.cjs | 6 (3 start routes × 2 link types) | Chrome launched per case, 3.4–4.5 s each. | BEHAVIOR 2, DUP 4 | Keep 2: repository link from `/` and model link from `/work/…`, sharing one browser. | Med |
| health-ui.test.cjs | 1 | Chrome launch. React bundle. | BEHAVIOR | Keep. Drop the 4 "removed card is absent" asserts. | High |
| mcp-ui.test.cjs | 1 | Chrome launch. Unconditional full-page screenshot. | BEHAVIOR (broken) | Fix the incomplete API stub; the shared harness defaults would cover it. Drop the `paddingTop` and DOM-order asserts and the screenshot. | Med |
| navigator-ui.test.cjs | 2 | Chrome launched per test. 2 sleeps of 250 ms. No default timeout, so each failure waits 30 s. | DUP 1, LOGIC 1 (both broken) | Delete :13 (dead selectors; restoration is covered elsewhere). Replace :72 with a node test of the PR-link renderers in `web/src/index.tsx`. | High |
| tl1-ui.test.cjs | 1 | Chrome launch. 4 unconditional screenshots. | BEHAVIOR | Keep. Drop the screenshots and the "no chips" check. | High |

---

## 2. Per-test actions

### 2a. Delete

| Test (file:line) | Class | Why | What preserves coverage |
|---|---|---|---|
| transcript-ui:35 `token usage is a top-level tab instead of a Health table` | CD | Guards a removal from the original import (`#health table` count 0) plus DOM structure. Has its own copy-pasted page setup. | The server route is covered by Go `TestAppRoutesServeUIOnDirectNavigation`; Usage-tab navigation is covered by usage-ui. |
| transcript-ui:89 `token badges sit in a fixed-width trailing column…` | CD | Asserts equal pixel `left`/`width` across rows and DOM child order. It measures in a test-only layout with the app shell hidden by injected CSS, in Chrome, while the app ships in WKWebView. | Nothing functional is lost. A visual check can replace it if wanted. |
| transcript-ui:780 `message marker and operation content are vertically centered` | CD | ≤1 px centering. | Same as above. |
| transcript-ui:1105 `agent raw access is inline and message prose has stronger hierarchy` | CD | Checks `position:absolute`. The raw-event toggle is already covered. | :885 opens `.raw-event`. |
| transcript-ui:1012 `saved feedback and an unfinished annotation survive a page refresh` | DUP | Same as feedback-ui:59 (draft survives reload) and :276 (saved note survives reload). | feedback-ui:59, :276. |
| transcript-ui:1035 `feedback copy uses the macOS clipboard bridge…` | DUP | Same as feedback-ui:227. | feedback-ui:227. Move its `/# Pharos UI feedback/` check there, or into a node test of `feedbackMarkdown`. |
| transcript-ui:1056 `feedback copy reports clipboard failure…` | DUP | Same as feedback-ui:262. | feedback-ui:262. |
| feedback-ui:323 `expanded feedback stays reachable near viewport edges…` | DUP | Its only assertion is the in-viewport `waitForFunction`; the rest is 3 screenshots. | feedback-ui:130 already checks clamping after a resize. |
| library-drive-ui:389 `shows drive space and activity without a Disconnecting section` | CD | Checks that removed "Disconnecting/Eject" text is absent and that the panel says "Nothing is running." | incremental-sync:12 and library-drive:453 check "Nothing is running." |
| library-drive-ui:267 `shows disk space in the drive panel and links Sources to the one-off sync controls` | DUP (failing, 31 s timeout) | The Sources → Sync link and button placement are the same assertions as incremental-sync:238. | incremental-sync:238, plus a stubbed drive-panel test that checks `dt/dd` byte formatting. |
| library-drive-ui:347 `backs up from Health, refusing the library drive…` | DUP | The refusal reasons are Go logic. | Go `TestBackupRefusesUnsafeDestinations` and `TestBackupRefusesDiskImagesOnTheSameDrive`. The finished/partial rendering already uses a stubbed `/api/backup`, so keep about 10 lines of it in a stubbed page. |
| library-link-ui (4 of 6 generated cases) | DUP | All three start routes reach Library while it is still unmounted, through the same `pharosNavigate` + `q_library` restore path. | Keep repository from `/` and model from `/work/work-1`. |
| navigator-ui:13 `navigator restores routes, library search, view, and query filters` | DUP (broken) | The searchbox `Search text and related terms` exists nowhere in ui.py, web/src or the bundle. `#navigatorToggle` is `hidden` unless a preference is set. Two 250 ms sleeps. | URL and query restoration: library-link-ui, transcript:431, delegation:1199/1303, machine-activity:256. If the navigator "Go" box still matters, write a 10-line test for it. |
| machine-activity-ui:390 `No repository includes null and empty calls…` | DUP | The logic is in web/tests/machine-activity.test.mjs:128, :137 and :147 (replace, toggle-off and repeated segments). | Those node tests. Add one "reload keeps (No repository)" assertion to :256. |

### 2b. Move down to a node unit test

Prerequisite for the transcript items: a node-loadable reader model (§3c). Everything
listed already runs inside `page.evaluate` or depends only on DOM-free functions.

| Test (file:line) | Class | Pure function(s) | Node replacement |
|---|---|---|---|
| transcript:29 `all source accounting formats reconcile against shared fixtures` | LOGIC | `conversationTokenReports` (ui.py:759) | A node parity test over `token-usage-fixtures.json`, the JS half of Go `TestTokenUsageFixtures`. |
| transcript:500 `reader classification and find counts match the shared fixtures…` | LOGIC | `conversationMessages`, `transcriptStructure`, `transcriptFind`, `transcriptFindPattern` | A node parity test, the JS half of `internal/archive/transcript_find_test.go`. |
| transcript:569 `events inside a hidden sub-agent count as hidden` | LOGIC | `transcriptStructure` | Add the case to `transcript-find-fixtures.json` so Go checks it too. |
| transcript:80 `split message usage includes cache once…` | LOGIC | `conversationTokenReports`, `tokenTotal` | Node: 1330 total, one context sample. |
| transcript:199 `context profiles isolate streams, reset at compaction…` | LOGIC | `contextTokenReports`; `attachTokenBadge` text only | Node for the samples and deltas. Keep the tooltip text check in a render test if wanted. |
| transcript:243 `context rail widths are shares of the context window…` | LOGIC (+CD) | `contextWindow`, share calculation | Node for the 25%/50% shares and the 200K title. Drop the asserts on `::before` color, `::after` content and empty `innerText`. |
| transcript:223 `context rail has linear widths, compressed collapsed profiles…` | CD (+LOGIC) | Same share calculation | Node for the 50/100/25% widths. Delete the `--context-profile` string, `::before backgroundImage` and `collapsed-context` count asserts. The marker click that opens a turn is covered by :62 and :159/:1299. |
| transcript:263 `context windows come from Codex checkpoints…` | LOGIC | `contextWindow` | Node, verbatim. |
| transcript:276 `nested subagent checkpoints count once…` | LOGIC | `conversationTokenReports` | Node for total 300. Lazy nested-run building is covered by :794 and :811. |
| transcript:636 `JSON-string command arguments…` | LOGIC | `toolPayload`, result pairing, `messageFailed` | Node. |
| transcript:899 `Write without prior contents does not invent line deltas` | LOGIC | `diffStats` / edit-delta helpers | Node. |
| transcript:911 `normalized user-role text-block results remain paired…` | LOGIC | `normalizeMessages`, `messageFailed` | Node. |
| transcript:923 `null blocks are tolerated and 'exit code' text does not signal failure` | LOGIC | `normalizeMessages`, `messageFailed` | Node. |
| transcript:934 and :946 (aborted-by-user noise) | LOGIC | `trimTrailingConversationNoise` | One node table test. |
| transcript:955 `failed Edit labels the requested diff…` | LOGIC | Summary label derivation | Node for "Failed" and "Requested change". The click-to-diff interaction stays in :648. |
| transcript:967 `replace_all and deletion … unknown delta counts` | LOGIC | Delta helpers | Node. |
| transcript:1091 and :1139 (todo summaries) | LOGIC | `describeTodoChanges` | Node for the labels. Keep one chip-preview click in a merged browser test. |
| transcript:1131 `bash icons distinguish git python typechecking and tests` | LOGIC (+CD) | `commandKind` | Node. Drop the "row height ≤26 px" check. |
| transcript:1155 `repository worktree and recorded cwd paths collapse…` | LOGIC | `conversationRoots` / prefix logic | Node for the prefixes. Clicking a prefix to expand it is covered by :993. |
| transcript:1342 `parsed message bodies are reused…` | LOGIC | `messageJSON`, `normalizeMessages`, `toolPayload` | Node, verbatim; it is already all in `page.evaluate`. |
| delegation:1093 `First evidence uses local time and readable tooltips…` | LOGIC | `formatted`, `cellTitle`, `localTime` (delegation.tsx:136–200, not exported) | Export them; node test with `TZ=America/Denver`, following the pattern in `tests/chart-period.test.cjs`. |
| carbon:14 and carbon:161 (all exact grams/Wh/OE figures) | LOGIC | `calculate` via `window.pharosCarbon.estimate/energy/mass` | Node vm test covering each grid, PUE, estimate, input-sensitivity and range combination. Keep one browser test that checks the controls recalculate, persist across reload, and that dialogs return focus. JS math is not covered elsewhere: Go `TestCarbonEnergyArithmetic` checks the separate Go implementation. |
| incremental-sync:75 `Home cards refresh only when a finished run indexed something` | LOGIC | `announceChanges` (sync.js:212) | Extract `finishedRunsChanged(prevIds, runs)` and test it as a node table. That removes 9 polls of at least 2 reads each, with 25 ms sleeps. |
| incremental-sync:12 `The header stops spinning when work finishes after leaving Settings` | LOGIC (flaky) | `renderHeader` busy derivation | Node for "busy iff a run is running". Drop `hostReads === readsBeforeFinish`, the racy exact request count that makes it flaky. |
| incremental-sync:355 `Sync prioritizes readable history…` | LOGIC (+BEHAVIOR) | sync.js duration, bytes and relative-time formatters | Node for `22s`, `<1s`, `1m 2s`, `1h 1m`, `129 MiB` and `2m ago`. Keep a short browser test that a refresh preserves open details, `scrollLeft` and focus. |
| navigator:72 `Past Work shows linked GitHub PRs and origin/main merges` | LOGIC (broken) | `pullRequests`, `pullRequestURL`, `githubPullURL`, `mainIntegration` (index.tsx:83–105) | Export them and unit-test with esbuild. |
| findings:250 (empty-state sentences only) | CD | `EmptyOpen` copy builder (findings.tsx:993) | Delete the copy asserts or unit-test the sentence. Move the settings link and detector re-enable POST check into :182's session. |

### 2c. Merge (fold one into another to save a page load)

| From → into | Class of "from" | Reason |
|---|---|---|
| transcript:159 → :1299 | DUP | Both check that a single long turn opens by default and pages its events. delegation:1176 also covers "opens on first visit". |
| transcript:451 → :467 | DUP | :467 also does Ctrl+F, the others list and Next across conversations. |
| transcript:522 → :580 | DUP | Both press "Next failed action" and check the error row opens. |
| transcript:732 → :674 | DUP | :674 already folds `.xml-node` and restores the chip. Drop the `::after` `"…</files>"` check. |
| transcript:107 → :113 | CD | Same "no usage" scenario. |
| transcript:120 (model-chip click only) → :384 | CD | :384 already checks the repository chip filters Library. Drop the ±5 px header-geometry asserts. |
| transcript:853 + :1228 | INTEG | One rails test that checks scroll sync and that no rail overlaps the header. Loosen the ±1 px tolerances and replace `waitForTimeout(100)` with a style-change wait. |
| delegation:1040 → :1018 | BEHAVIOR | Same fixture style. Two `fixture()` calls in one test still save setup. |
| delegation:1354 → :1332 | BEHAVIOR | Complementary cases of the lone-conversation layout. |
| delegation 390 px overflow checks (:469, :584, :977, :1234) → :1234 | CD | One mobile test instead of four `setViewportSize` checks. |
| feedback:88 → :59 | DUP | Drag, reload and same-position are already in :59. Keep the pin and toast steps. |
| machine-activity:213 → :256 | DUP | Both do the double-zoom. :256 already covers history; keep :213's Reset assertions. |

### 2d. Keep, but strip change-detector assertions (most common breakage source)

- transcript: :62 heat-color `color-mix` regex; :141 opacity polling and `#ph-icon-check`
  href; :384 `turn.y < 450`; :593 timestamp/marker ≤3 px; :746 two-line height, exact
  class string, opacity and `waitForTimeout(250)`; :811 `--token-color` differs; the
  3 "Highest-spend turn button is absent" asserts (:70, :172, :189).
- delegation: :391 (57 cells, no `%`, `color-mix`); :469 (6 absent labels,
  `paddingLeft [4,12,20]`, "Tint · All columns", heading/totals geometry); :648 (frame
  line counts, 1 px divider, 8 px side delta); :743 (`border 0px`, `radius 0px`,
  `x === reader.x - 8`, `maxHeight none`); :785 (lane `45` px, absent Zoom/Pan buttons
  and datetime inputs, `backgroundImage none`); :925 (9 category labels, mark
  border/shadow); :1176 (transparent background); :1332 (child class-name list).
- carbon: operator glyph lists, `aria-label` strings, `.carbon-info-button` count 7,
  select width ≤52, `dotted` border, `borderBottomWidth 0px`.
- feedback: `rgb(234, 176, 74)` and red ≠ yellow; panel has no `header`; height > collapsed+100.
- findings: tab text `Optimize2`, nav order, impact-column geometry, `innerHTML` contains `<code>`.
- incremental-sync: radius `4px`, chevron/`30px` padding, header child IDs, `animationName`,
  exact request counts, absence of removed buttons.
- mcp: `paddingTop 0px` and DOM order. tl1: `.tl1-chip` count 0. health: 4 removed-card checks.

### 2e. Keep as is (genuinely valuable browser tests)

- **INTEGRATION-ESSENTIAL:** transcript:836 (no horizontal overflow at 390 px),
  :853/:1228 (rail scroll sync); delegation:785 (brush, pan, Escape-cancel; fix its 7 sleeps),
  :925 (popover focus returns on Escape), :1199 (history restores scroll and open
  turns), :1234 (modal sheet focus trap, `inert`, Escape returns focus); feedback:59,
  :130, :156 (pointer drag, keyboard nudge, top-layer `<dialog>`); library-drive:155
  and :180 (true E2E onboarding and cross-Mac index, opt-in only).
- **High-value BEHAVIOR:** transcript:1117 (Markdown does not run `<script>` or
  `javascript:` links; a security guard); delegation:1279 and :1364 (stale-response
  races); carbon:293 stale response, invalid dates never query, focus kept while typing
  dates (split it up); findings:182 (clipboard written before measuring starts);
  health:13 (sections render independently and one failure is isolated);
  transcript:533/:1170 (Show filter, find, regex and case flows).

---

## 3. Harness duplication and a shared-harness proposal

### 3a. What is copy-pasted

| Snippet | Files |
|---|---|
| `createRequire('.context/browser-tests/package.json')('playwright')` | 13/13 (library-drive also searches parent dirs and `PHAROS_BROWSER_TESTS`) |
| `html = source.slice(source.indexOf("r'''")+4, source.lastIndexOf("'''"))` | 12/13. transcript and navigator still probe the dead `src/pharos/ui.py` path. |
| Hard-coded Chrome path plus `existsSync` fallback | 22 copies across 13 files. Only 3 files honor `CHROME_PATH`. delegation-ui has no fallback and fails without Chrome. |
| `/assets/*` handler | 4 variants: all assets from disk via `readFileSync` per request (1.7 MB each time); only `query-tables.*`; `fulfill({path})`; or every JS file stubbed as `''` (transcript, feedback). |
| API fallback body | 6 variants: `{items:[]}`, `'{}'`, a 404, `{items,rows,metrics,counts,setup,usage}`, the long delegation default, `{rows:[],total:0,metrics:[]}`. |
| `/api/tools/status`, `/api/upgrade`, `/api/library/status`, `/api/sync/status` stubs | Repeated 2–6 times (incremental-sync has 6 near-identical route tables). |
| `pageerror` collection plus `assert.deepEqual(errors, [])` | 12/13, but carbon and incremental-sync filter to their own asset's stack, and incremental-sync:12/:75 and navigator:72 have no listener. |
| `window.webkit.messageHandlers.pharosClipboard` stub | transcript (2×), feedback, findings, mcp. |
| Screenshot opt-in | 6 env-var names (`TRANSCRIPT_SCREENSHOTS`, `HEADER_SCREENSHOT`, `CONTEXT_SCREENSHOT`, `PICKER_SCREENSHOT`, `RAIL_SCREENSHOT`, `PHAROS_UI_SCREENSHOTS`) plus 26 unconditional calls. |
| Scroll-settle helper (200 ms debounce) | machine-activity ×3, inline. |

### 3b. Proposed API (`tests/browser/harness.cjs`)

```js
// One Chrome per run, many isolated contexts.
const ui = require('./browser/harness.cjs');

// Process-wide, lazy, cached:
//   ui.browser()    -> connects to PHAROS_BROWSER_WS (set by the runner) or launches once;
//                      closes on process exit. Honors CHROME_PATH, else Chrome, else bundled.
//   ui.html         -> APP_HTML from internal/archive/assets/ui.py, extracted once.
//   ui.asset(name)  -> Buffer cache of internal/archive/assets/* (no per-request disk reads).

test('Findings routes and counts open findings', async t => {
  const app = await ui.open(t, {
    url: '/findings',                       // any non-/api, non-/assets path serves ui.html
    viewport: { width: 1400, height: 1000 }, timezoneId: 'America/Denver', locale: 'en-US',
    bundle: 'real',                         // 'real' | 'stub' (empty query-tables.js, as transcript does)
    assets: 'all',                          // 'all' | ['sync.js','library.js'] | 'none'
    api: {                                  // method + path pattern -> body | (req, params, state) => body
      'GET /api/findings': () => state.overview,
      'POST /api/findings/:id/action': req => ({ ok: true }),
    },
    // Defaults cover every shell endpoint (tools/status, upgrade, library/status,
    // sync/status, sync/issues, sources, index, capture, activity, pricing, mcp,
    // preferences) and query-table endpoints (rows/aggregations/distinct/field-stats),
    // so a stub can never be "incomplete" (the MCP failure).
    storage: { 'pharos-carbon-chart': {...} },   // seeded via addInitScript
    clipboard: true,                        // app.clipboard -> texts written via the webkit bridge
    errors: 'all',                          // asserted in t.after; opt out with a RegExp filter
    timeout: 5_000,                         // page.setDefaultTimeout, so broken tests fail fast
  });
  // app.page, app.requests ([{method, path, query, body}]), app.api.set(pattern, handler),
  // app.api.hold(pattern) -> { started: Promise, release() } for race tests,
  // app.settle() -> two animation frames (replaces fixed sleeps),
  // app.shot(name) -> no-op unless PHAROS_UI_SCREENSHOTS=dir.
});

// Reader fast path for transcript-ui: one loaded page per file.
const reader = await ui.reader();   // before(): ui.html with bundle stub, SHARE search swap
test('…', async t => {
  const panel = await reader.render(t, messages, conversation);  // resets root, localStorage,
  // feedback state and transcript filter; reloads only when t.options.fresh is set.
});
```

Runner (`tools/ui-tests.mjs`, Node 20 compatible; there is no `--test-global-setup` until Node 24):

```js
const server = await chromium.launchServer({ headless: true, executablePath });
const env = { ...process.env, PHAROS_BROWSER_WS: server.wsEndpoint() };
spawnSync(process.execPath, ['--test', `--test-concurrency=${jobs ?? 3}`, ...files], { stdio: 'inherit', env });
await server.close();
```

Layout:

- `tests/ui/*.test.cjs`: stubbed pages through the harness.
- `tests/e2e/library-drive.test.cjs`: skipped unless `PHAROS_E2E=1`.
- Pure tests: `tests/*.test.cjs` or `web/tests/*.mjs`.

Capping `--test-concurrency` at 3–4 with a single Chrome removes most of the CPU
oversubscription that inflates timings (4.6 s → 13.3 s for the same delegation test).

### 3c. Making ui.py logic node-testable

- **Preferred:** move the DOM-free reader model into
  `internal/archive/assets/reader-model.js`, a classic script that defines globals
  like `sync.js` does. Candidates: `messageJSON`, `parsedJSON`, `normalizeMessages`,
  `conversationMessages`, `trimTrailingConversationNoise`, `toolPayload`,
  `tokenBreakdown`, `tokenTotal`, `tokenEnvelope`, `conversationTokenReports`,
  `contextTokenReports`, `contextWindow`, `TRANSCRIPT_CATEGORIES`,
  `transcriptStructure`, `transcriptFind*`, `describeTodoChanges`, `diffStats` and
  `commandKind`. They sit in roughly ui.py:715–866, 885–937 and 1161–1205. Load it in
  node with `vm.runInNewContext(src, ctx)`.
  - `share.go` must inline the new file into the export. `share_test` already
    asserts there are no `/assets/` references.
- **No product change:** a test helper that brace-matches top-level `function NAME(`
  blocks in the extracted `<script>` and evaluates the named closure in a vm. It is
  fragile but has precedent: transcript-ui:14 already slices
  `setupConversationSearch` out of the HTML.
- `carbon.js` and `sync.js` need only a minimal `window`/`document`/`localStorage`
  stub. The precedent for global stubs is `web/tests/concurrency.test.mjs:242`.

---

## 4. Surprises

**Broken or flaky on master** (2026-10-18 full run: 254 browser tests, 17 failing;
causes verified by reading where possible):

- findings-ui:100 and :250 fail.
  - The nav now starts with a `home` tab (ui.py `<nav class="tabs">`), so the
    asserted order `['library','usage','tools','findings','mcp']` can never match.
  - The asserted copy "saved a month" no longer exists in `findings.tsx` (it now
    renders `Expected: … a month`).
- incremental-sync-ui:117 and :238 fail every run.
- incremental-sync-ui:12 is flaky on the untouched baseline (diffed between two
  runs on 10-18). The likely cause is its exact `/api/index` read-count assertion
  racing library.js polling.
- library-drive-ui (the `describe`) fails. On 10-15, `onboards a new Mac` (62 s),
  `another Mac's captures` (31 s) and `disk space in the drive panel` (31 s) failed
  on timeouts. The suite took 163 s.
- mcp-ui:13 fails. The earlier wording of `docs/query-table-local-development.md`
  attributes this to an "incomplete MCP API stub".
- navigator-ui:13 and :72 fail. The searchbox label and the visible toggle no longer
  exist (see 2a). :13 waits on hidden elements with no default timeout, so it costs
  at least 30 s per run.
- Doc mapping, for my scope: MCP = mcp-ui, navigator = navigator-ui, Findings =
  findings-ui, Sync = incremental-sync-ui. The doc's "Library" is mostly
  library-query-ui (out of scope), but library-drive-ui also fails. "Usage badge",
  "pricing catalog", "card library" and "Human Words" are other files.

**Tests that cannot fail, or fail misleadingly:**

- feedback:323 asserts almost nothing; it exists to take 3 screenshots.
- delegation-ui's route handler calls `assert.equal(url.searchParams.get('limit'), '40')`
  inside `page.route`. If that ever fails, the request is never fulfilled and the test
  hangs until a timeout instead of reporting the assertion.
- carbon and incremental-sync only record `pageerror`s whose stack names their own asset
  (`/assets/carbon.js`, `sync.js`), so React and `ui.py` exceptions pass silently.
  incremental-sync:12/:75 and navigator:72 register no listener at all.
- transcript:178 is titled "…highest-spend navigation" but asserts that the
  Highest-spend button is absent. The same absence is asserted 3 times in the file.
- delegation's fake server implements sorting and collapse. The row-order checks after
  clicking sort (e.g. :648 waits for `s-nested` first) partly test the fake.
- machine-activity:53, :213 and :256 use `machine_demo=1`. The page then shows built-in
  demo data, so the carefully stubbed `rows` are unused in those tests. The demo
  generator is already unit-tested in `machine-activity.test.mjs`.

**Measuring the wrong thing:**

- The whole browser suite runs in Chrome, but the shipped Mac app renders in
  WKWebView (`macos/PharosApp.swift`). Every pixel, CSS or `getComputedStyle`
  assertion is verified on a different engine. Only feedback-ui has an opt-in
  `FEEDBACK_BROWSER=webkit`.
- transcript-ui injects CSS that hides the entire app shell, so its alignment and
  geometry assertions measure a layout the app never shows. :1228 has to re-show the
  header with a second style tag to test rail overlap.

**Fixed sleeps:**

| File | Lines | Sleep |
|---|---|---|
| transcript | :772, :1276 | 250 ms, 100 ms |
| delegation | :836, :855, :858, :880, :886, :892, :900 (all in one test), :1296, :1384 | 100–150 ms |
| navigator | :60, :65 | 250 ms, used as "wait for queries"; should be `waitForRequest` |
| incremental-sync | :62, :101 | 25 ms polling loops |
| carbon | :527 | 10 ms busy-wait, up to 100 iterations |
| machine-activity | :152, :175, :335 | 200 ms scroll-debounce promises |
| library-drive | `waitFor` | 200 ms polls with 30 s caps; `idle()` polls every 500 ms for up to 180 s |

**Timing assertions and huge fixtures:**

- No test measures elapsed time.
- Huge fixtures in transcript: :1317 (2,001 messages), :1354 (2,202), :1299 (351),
  :159 (303), :1281 (110), :178 (93).
- library-drive seeds 600 sessions and runs `go build` inside `before()`. That build
  is CPU-heavy and competes with benchmarks.

**Screenshots:**

- 26 unconditional, never-compared screenshots:

  | File | Count |
  |---|---|
  | carbon | 9 |
  | incremental-sync | 5 |
  | tl1 | 4 |
  | feedback | 3 |
  | machine-activity | 3 |
  | findings | 1 |
  | mcp | 1 |

- 15 more are env-gated (transcript ×11 behind 5 env names, findings ×3, health ×1),
  plus library-drive's `shoot()`.
- Two tests change state just to take a screenshot. findings:100 sets a 1920×1055
  viewport and then keeps asserting layout at that size. carbon:161 resizes to
  1280×1400 and 390×844 around its screenshots. Removing those screenshots requires
  care.

**Order and environment dependence:**

- library-drive tests depend on order ("the second expects the Mac the first
  onboarded").
- library-drive's eject test spawns `python3` holding a `flock` for up to 60 s.
- delegation-ui needs Chrome at the hard-coded path.

**Duplication with other test layers:**

- Go:
  - `token_usage_test.go` and `transcript_find_test.go` deliberately share the two
    fixture JSONs. transcript:29 and :500 are the JS half of that parity; keep them,
    as node tests.
  - `backup_test.go` and `drive_test.go` cover library-drive's backup and drive-check
    logic.
  - `preferences_test.go` covers the server half of library-drive:298.
  - `devui_test.go`, `share_test.go` and `archive_test.go` only assert trivial HTML
    (`<!doctype`, brand, `id="navigator"`), so there is no overlap with page logic.
- web/tests:
  - `machine-activity.test.mjs` duplicates machine-activity-ui:390. It also covers the
    "+N% per run / 0% calls" facts in :53 ("raw examples reconcile … slowdown from
    volume").
  - `concurrency.test.mjs` covers zoom/settings normalization from :213.
  - `delegation-review.test.mjs` covers the Home review card, not the explorer. There
    are **no** node tests for `delegation.tsx`, `findings.tsx`, `carbon.js` or `sync.js`.

---

## Appendix: classification of every case

**transcript-ui (70)**

- BEHAVIOR (28): 62, 113, 141, 178, 384, 407, 415, 431, 467, 511, 533, 580, 593, 613,
  648, 660, 674, 723, 794, 811, 872, 885, 977, 993, 1075, 1117, 1170, 1281
- LOGIC (21): 29, 80, 199, 243, 263, 276, 500, 569, 636, 899, 911, 923, 934, 946, 955,
  967, 1091, 1131, 1139, 1155, 1342
- CD (8): 35, 89, 107, 120, 223, 746, 780, 1105
- DUP (7): 159, 451, 522, 732, 1012, 1035, 1056
- PERF (3): 1299, 1317, 1354
- INTEG (3): 836, 853, 1228

**delegation-ui (23)**

- BEHAVIOR (14): 584, 977, 1018, 1040, 1049, 1115, 1151, 1176, 1266, 1279, 1303, 1332,
  1354, 1364
- CD (4): 391, 469, 648, 743
- INTEG (4): 785, 925, 1199, 1234
- LOGIC (1): 1093

**Smaller files**

| File | BEHAVIOR | LOGIC | CD | DUP | INTEG |
|---|---|---|---|---|---|
| carbon-ui | 293 | 14, 161 | | | |
| feedback-ui | 204, 227, 262, 276, 306, 346 | | | 88, 323 | 59, 130, 156 |
| findings-ui | 182, 228 | | 100, 250 | | |
| machine-activity-ui | 53, 134, 256, 326 | | | 213, 390 | |
| incremental-sync-ui | 238, 470 | 12, 75, 355 | 117 | | |
| library-drive-ui | 239, 298, 412, 453 | | 389 | 267, 347 | 155, 180 |
| library-link-ui | 2 kept cases | | | 4 matrix cases | |
| health-ui | 13 | | | | |
| mcp-ui | 13 | | | | |
| navigator-ui | | 72 | | 13 | |
| tl1-ui | 12 | | | | |
