> Working notes behind [the test suite survey](../testing-survey.md), written by one of the five 2026-10-19 reviews at `bb496be`. Line numbers refer to that commit. Classifications are judgement calls; the timings here are indicative, taken from contended runs or the Pharos archive. See the survey for measured numbers.

# Go test audit: `internal/archive` (mcp_latency … writing_table), `internal/querytable`, `cmd/`, `third_party/query-table-go`

Scope: 59 archive test files (`ls internal/archive/*_test.go | sed -n '59,117p'`), 16,037 lines, **331 `Test*` + 4 `Benchmark*`**; `internal/querytable` (3 files, 16 tests); `cmd/` (no tests); `third_party/query-table-go` (19 files, 124 tests).
No tests were run for this audit. Timings come from the existing `.context/survey/go-test.json` (one `go test -json ./...` run, 2026-10-19 06:56, probably under load). Treat absolute seconds as noisy and the ranking as reliable.

## 0. Findings that matter most

1. **The archive package does not finish under the default 10-minute `go test` timeout.** `go-test.json` ends with `panic: test timed out after 10m0s` inside `TestTextSearchTimeoutRequiresProgress/first_row/resumed=false`. Wall time was 607 s, but user+sys was only 174 s (`go-test.time`), so CPU was busy about 29% of the time. The suite spends most of its time **waiting**: fsync/F_FULLFSYNC, lock holds, sleeps, and subprocesses. In this scope, 217 tests ran and took 189 s. **113 tests never ran**: everything alphabetically after `text_search_cursor_test.go`.
2. **Each fixture costs a fixed amount, and that dominates.** The 161 in-scope tests that build a catalog had a median of **0.67 s** (sum 182 s). The 56 pure tests summed to 6.7 s. `testCatalog` → `OpenCatalog` → `Initialize` runs the whole DDL batch, about 57 `hasColumn` PRAGMAs, and the migrations. It also re-creates **138 cache triggers** (`ensureQueryCacheGeneration`, query_cache_generation.go:46). All of this runs with `synchronous(FULL)` + `checkpoint_fullfsync(1)` (durability.go:44). `Close()` then runs a TRUNCATE checkpoint (catalog.go:129). The catalog's own contention log twice reported `operation=query-cache-schema held=8.553s / 6.552s` during a plain `testCatalog`.
3. **A few seed sizes account for about 30 s of the measured scope**:
   - 10,031 tool calls inserted row by row (13.8 s).
   - 25,001×3 FTS rows to get past a hard-coded `LIMIT 25000` (11.2 s).
   - 10,000 snapshot rows to reach a `const` quota (5.8 s).
   - 3,000 calls plus a matrix of about 430 aggregations (`TestToolCallCubeAnswersLikeTheCalls`, not reached in this run).
4. **`third_party/query-table-go` tests never run** from the root. It is a nested module, and `go-test.json` lists only `cmd/pharos`, `internal/archive` and `internal/querytable`. All 19 test files are **byte-identical to the published v0.8.0 module** in the module cache. None of them exercises a Pharos-local patch.
5. **Pharos already contains copies of upstream's own sqlite-tests.** `query_table_v3_adapter_review_test.go` (`TestVendoredV3*`) is a line-for-line copy of `backends/sqlite-tests` in the owner's checkout (`~/gbdubs/query-table`). Two V2 tests are near copies too. They were written while V3 was a vendored pre-release (95862d4). Ironically, they are also the only tests that drive the patched fork's grouped-summary path.

---

## 1. Per-file table

Cost key: **s** = measured seconds (sum of top-level tests). **n/r** = not reached before the 10-minute timeout. **fixture** = one `testCatalog`-style catalog per test, about 0.3–1 s each.

| file | #tests | lines | measured | cost drivers | dominant class | recommendation | conf. |
|---|---|---|---|---|---|---|---|
| mcp_latency_test.go | 1 | 235 | skip | opt-in `PHAROS_MCP_BENCH`; package-level `benchWords` is built at every binary start | PERF/BENCH | Move behind `//go:build probe` (or tools/). Dedupe `toolTextOf` with `toolText`. | high |
| mcp_library_test.go | 14 (1 helper) | 938 | 27.3 s | `testLibrary` per test (InitLibrary+OpenCatalog+ingest+library refresh); `lsof` subprocess at each `holdsCatalog`; `/usr/bin/python3` flock holder; re-exec of the test binary; 5 s sleeping host stub; wall thresholds of 2 s / 1 s / 5 s | INTEGRATION-ESSENTIAL (drive unplug, release, SIGBUS restart, forwarding parity) | Keep. Copy a prebuilt library template instead of calling `testLibrary` per test. Replace the python flock with an in-process flock on a second fd. Investigate the 6.3 s `TestMCPForwardingDeclinesOtherServices`. | high |
| mcp_provenance_test.go | 11 | 440 | 5.0 s | `auditFixture` per test; about 1 MB of text re-chunked over about 85 MCP calls | BEHAVIOR (MCP budget/pagination/lineage contract) | Keep. Shrink `strings.Repeat(…, 60000)` to about 3,000. Use the template fixture. | high |
| mcp_repositories_test.go | 2 | 97 | 0.4 s | fixture | BEHAVIOR (agent-facing error text is a contract) | Keep | high |
| mcp_search_test.go | 2 | 315 | 0.4 s | 150-line re-implementation of the old search (`referenceSearchConversations`, :16) | CHANGE-DETECTOR (frozen legacy oracle) | Replace the oracle with explicit expected orderings on the 5-conversation fixture | medium |
| mcp_statistics_test.go | 21 | 661 | 18.9 s | 11.2 s in one test (25,001×3 inserts); `statisticsFixture` ×18 | BEHAVIOR | Keep all. Make the 25,000 lexical cap a var and set it to about 50 in the test. | high |
| menu_bar_test.go | 6 | 196 | 1.2 s | fixture | BEHAVIOR | Keep | high |
| message_origin_test.go | 5 | 195 | 0.2 s | parse-only (4/5) | BEHAVIOR | Keep. Good candidate for a leaf parser package. | high |
| message_search_filter_test.go | 2 | 99 | 0.4 s | textFixture | BEHAVIOR (one SQL-text assert, `"0=1"`, :59) | Keep. Assert zero rows instead of SQL text. | medium |
| model_effort_test.go | 5 | 348 | 1.8 s | fixture; `init()` registers an SQLite function; EXPLAIN index-name check | BEHAVIOR + perf guard | Keep. Move the EXPLAIN test into the consolidated plan-guard table. | medium |
| naming_audit_test.go | 4 | 204 | 6.5 s | 7 subtests, each with a fresh catalog + ingest + `runCapture` + audits | INTEGRATION-ESSENTIAL (audit must catch transcript corruption) | Keep. Use the template catalog. Optionally trim 5 store variants to 3. | high/medium |
| origin_test.go | 2 | 64 | 0.6 s | 2 fixtures | BEHAVIOR (CSRF/origin security) | Keep. Merge into one catalog and one table. | high |
| performance_probe_test.go | 3 | 120 | skip | opt-in `PHAROS_QUERY_PROBE`; t.Log only | PERF/BENCH | Move behind build tag `probe` | high |
| portable_test.go | 8 | 303 | 1.4 s | InitLibrary/OpenCatalog | BEHAVIOR (wrong-volume and newer-schema refusals protect data) | Keep | high |
| preferences_test.go | 2 | 67 | 1.0 s | fixture; exact JS body | BEHAVIOR (`window.pharosPreferenceValues=` is the page contract) | Keep | high |
| pricing_test.go | 6 | 224 | 3.1 s | fixture + `ingestUsageFixture` | BEHAVIOR (+ validates the checked-in pricing file) | Keep | high |
| probe_test.go | 10 | 538 | 7.3 s | real `sh` scripts with 300 ms–2 s timeouts; threshold `>1500ms` (:267); chmod-0 dirs | BEHAVIOR (privacy: never enter protected folders) | Keep. Loosen the :261/:267 thresholds to timeout+1 s. | high/medium |
| query_cache_generation_test.go | 9 | 279 | 16.1 s | one test at 9.8 s, with 8.6 s inside the `query-cache-schema` writer; `libraryFixture` | BEHAVIOR (cache invalidation correctness) | Keep. Replace the EXPLAIN in `TestConversationChildrenUseCoveringIndexAfterUpgrade` with an index-exists check, or move it to the plan table. | medium |
| query_rows_indexed_test.go | 5 | 185 | 6.6 s | seed 31/131; EXPLAIN; SQL-text join assertions | BEHAVIOR (indexed ≡ typed executor) + perf guard | Keep. Merge with the drill-down parity test (§2). | medium |
| query_table_exploration_test.go | 7 | 211 | 12.1 s | 5.8 s to insert 10,000 snapshot rows (const quota); 1.7 s on a disabled endpoint | BEHAVIOR + DUPLICATE/tombstone | Delete `TestQueryPopulationDownloadsDisabled`. Make `querySnapshotCount` a var. Fold the per-dataset smoke into one shared table. | high |
| query_table_metric_population_test.go | 3 | 154 | 2.1 s | seed 300 | BEHAVIOR (0.8 metric WHERE/minCount on every host path) | Keep. Seed about 100 and adjust `MinCount`. | medium |
| query_table_v2_review_test.go | 4 + 1 bench | 308 | 1.5 s | fixture | BEHAVIOR ("review" is only a historical name) | Merge into query_table_v2_test.go | high |
| query_table_v2_test.go | 10 + 2 bench | 728 | 5.0 s | seed 300/650; `init()` registers a probe function | Mixed: BEHAVIOR for the Pharos paths; 4 tests DUPLICATE upstream sqlite-tests | Keep the host tests. Relabel ReductionReuse/IndexedWindow as patch guards. Drop MetricMatrix/ComputedAndLimits. Seed 650 → 100. | medium |
| query_table_v3_adapter_review_test.go | 5 | 202 | 4.3 s | `testCatalog` used only as a scratch DB | DUPLICATE (upstream `backends/sqlite-tests`) | Delete once upstream sqlite-tests run against the fork (§3). Meanwhile use `sql.Open(t.TempDir())` (~4 s saved). | medium-high |
| query_table_v3_test.go | 8 | 338 | 25.7 s | 13.8 s seeding 10,031 rows one by one; 5.2 s setup in AllDatasets | BEHAVIOR (V3 host contract; asserts the patched 100k usage group budget) | Keep. Make the row budget injectable or insert in bulk. Table-drive the V2/V3 lease tests. | high |
| reclamation_test.go | 1 | 19 | never (build tag) | `//go:build reclamation` | SUPERSEDED (mothballed feature) | Leave with the mothballed feature; no runtime cost | high |
| repository_identity_test.go | 19 | 745 | 8.8 s | 10 tests open a full `OpenCatalog`; 2 run real git | BEHAVIOR (bad merges corrupt attribution) | Keep. Table-drive the 9 pure `planRepositoryMerges` cases. Share one git fixture. | high/medium |
| repository_location_test.go | 2 | 46 | 3.1 s | real git init/commit/worktree using the user's global git config | INTEGRATION-ESSENTIAL | Keep. Set `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1` in TestMain. | high |
| search_explain_test.go | 6 | 343 | 11.3 s | 7.6 s in one test (6.6 s in the query-cache-schema writer) | BEHAVIOR | Keep (template fixture) | high |
| semantic_test.go | 1 | 115 | 0.5 s | ingest | BEHAVIOR (vector back-compat) | Keep | high |
| service_test.go | 10 (1 helper) | 371 | 7.5 s | re-exec subprocess; 20 s polling deadline; 50 ms windows | INTEGRATION-ESSENTIAL (clean SIGTERM/WAL close, release) | Keep. Already `-short`-gated where needed. | high |
| share_test.go | 7 | 276 | 4.4 s | seed 300 | BEHAVIOR (privacy/XSS of exported HTML; not a change-detector) | Keep. Seed 300 → about 40. | high |
| skill_usage_test.go | 8 | 340 | 2.5 s | 5 pure tests | BEHAVIOR | Keep | high |
| source_toggle_test.go | 2 | 128 | 2.3 s | probe library | BEHAVIOR (lost-update/race on config files) | Keep (most valuable under `-race`) | high |
| sql_table_test.go | 5 | 226 | ~0 s | in-memory SQLite | BEHAVIOR; 4 parity twins of `internal/querytable` tests | Move to one dual-engine conformance table (§3) | medium |
| text_search_cursor_test.go | 5 (21 subtests) | 273 | killed | a fresh `textFixture` per subtest; 500 ms stall budgets; **was running when the 10-minute timeout fired** | BEHAVIOR (cursor safety ⇒ no silent loss) | Keep. Template fixture; TimeoutMS 500 → 150; check the `first_row` phase for a hang. | medium |
| text_search_preview_test.go | 1 | 32 | skip | opt-in; sleeps 2 minutes | PERF/BENCH (dev preview, not a test) | Move to tools/ or behind build tag `probe` | high |
| text_search_replica_test.go | 1 | 169 | skip | opt-in replica; wall thresholds 2.5–3 s | PERF/BENCH | Move behind build tag `probe` | high |
| text_search_review_test.go | 5 | 243 | n/r | 5 textFixtures | BEHAVIOR (review regressions, not superseded) | Merge into text_search_test.go. Drop the duplicate nesting assert. | medium |
| text_search_test.go | 12 | 469 | n/r | 9 textFixtures | BEHAVIOR | Keep (template) | high |
| timestamps_test.go | 4 | 288 | n/r | fixture + migration | BEHAVIOR (data normalization migration) | Keep. Move `TestSuppressMirrorsIgnoresInputOrder` to the library tests. | high |
| tl1_test.go | 10 | 681 | n/r | real TL1 sqlite fixtures, several ingests | BEHAVIOR / INTEGRATION | Keep | high |
| token_usage_test.go | 5 | 208 | n/r | ingest; JSON fixture shared with JS | BEHAVIOR (accounting) | Keep | high |
| tool_cube_test.go | 1 | 294 | n/r | seed 3,000; about 430 aggregations × 4 wheres × 2 engines + distinct + field stats | BEHAVIOR (differential oracle), expensive | Shrink the seed to about 500. Run the full op matrix only under `PHAROS_SLOW_TESTS`. | medium |
| tool_ledger_test.go | 14 | 567 | n/r | 9 pure parser tests | BEHAVIOR | Keep. Leaf-package candidate. | high |
| tool_search_test.go | 1 | 105 | n/r | seed 650 "more than one backfill batch"; EXPLAIN | BEHAVIOR + perf guard | Make the batch size a var and seed about 30 | medium |
| tool_urls_test.go | 4 | 111 | n/r | ingest | BEHAVIOR | Keep | high |
| transcript_find_test.go | 3 | 175 | n/r | JSON fixtures shared with JS | BEHAVIOR (cross-language parity) | Keep | high |
| usage_attribution_live_test.go | 1 | 60 | skip | opt-in; hard-coded workspace title "El Paso v10…" (:30); logs only | PERF/BENCH (one-off investigation) | Delete | high |
| usage_attribution_test.go | 6 | 269 | n/r | ingest | BEHAVIOR (non-writer copies must not change usage; WAL bound) | Keep | high |
| usage_conversations_test.go | 2 | 171 | n/r | fixture | BEHAVIOR | Keep | high |
| usage_performance_test.go | 4 | 143 | skip | needs `PHAROS_USAGE_PROBE`; `TestUsagePerformanceServer` is a blocking `ListenAndServe` (:142) | PERF/BENCH | Move behind build tag `probe` | high |
| usage_query_performance_test.go | 7 | 357 | n/r | seed 80; 5-subtest fixture rebuilds. **No timing assertions** despite the name. | BEHAVIOR (`TestUsageFusedSummariesMatchIndependentMetrics` is the host guard for the third_party fused-summary patch) | Keep. Rename the file (e.g. `usage_query_paths_test.go`). Share one catalog across the subtests. | high |
| usage_summary_test.go | 4 + 1 bench | 221 | n/r | EXPLAIN | BEHAVIOR + perf guard | Keep. Move the EXPLAIN to the plan table. | high |
| usage_table_test.go | 6 | 283 | n/r | `ingestUsageFixture`; the hourly fixture string is copied in 4 tests | BEHAVIOR | Keep. Add a `hourlyUsageFixture` helper. | high |
| work_reader_test.go | 7 | 291 | n/r | fixture | BEHAVIOR | Keep | high |
| workspace_names_test.go | 5 | 211 | n/r | fixture + reopen | BEHAVIOR (name durability) | Keep | high |
| writing_messages_test.go | 4 | 230 | n/r | fixture; hand-kept reason list | BEHAVIOR; `TestSpanRulesMatchTheSchema` is one-sided | Derive the reasons from the production table | medium |
| writing_table_test.go | 1 | 129 | n/r | fixture | BEHAVIOR | Keep | high |
| **internal/querytable**/apply_test.go | 13 | 293 | 0.2 s total pkg | pure | BEHAVIOR | Keep. Export the shared cases for the dual-engine conformance table. | high |
| internal/querytable/sort_test.go | 2 | 98 | ″ | pure, 200-trial property test | BEHAVIOR | Keep | high |
| internal/querytable/relative_time_test.go | 1 | 31 | ″ | pure | BEHAVIOR | Keep | high |
| **cmd/pharos** | 0 | – | – | `main.go` only calls `archive.Run` | – | Nothing to do | high |
| **third_party/query-table-go** (19 files) | 124 | 4,114 | never runs | nested module; 33 Postgres tests skip without `QT_TEST_POSTGRES`; `TestExampleSchemaLoads` / `TestRelativeDurationFixtures` need `../../schema/...`, which does not exist here | DUPLICATE (byte-identical to the v0.8.0 module zip) | Delete the copies and add an opt-in patch gate (§3) | medium-high |

The third_party files are all identical to `~/go/pkg/mod/.../backends/go@v0.8.0`:

| file | tests | file | tests |
|---|---|---|---|
| calendar_test.go | 3 | relative_time_test.go | 3 |
| compile_test.go | 26 | row_bindings_test.go | 5 (PG) |
| computed_store_test.go | 3 | set_filter_test.go | 2 (PG) |
| example_test.go | 1 (broken fixture path) | snapshot_test.go | 2 |
| metric_population_test.go | 3 | sqlite_compile_test.go | 10 |
| metrics_combined_test.go | 9 | sqlite_distribution_test.go | 2 |
| metrics_plan_test.go | 21 (15 PG) | sqlite_pharos_performance_test.go | 7 (upstream's file despite the name) |
| metrics_regression_test.go | 11 (10 PG) | sqlite_v2_test.go | 4 |
| plan_array_test.go | 2 (PG) | | |
| plan_performance_test.go | 7 | | |
| query_snapshots_test.go | 3 | | |

`diff` against the module cache shows only these patched files: `metrics.go`, `plan.go`, `query_v3.go`, `query_v3_execute.go`, `schema.go`, `sqlite_v2.go`. **No test file differs, so no third_party test guards a local patch.**

---

## 2. Per-test actions

### Delete

| test | class | why | what keeps the coverage |
|---|---|---|---|
| TestQueryPopulationDownloadsDisabled (query_table_exploration_test.go:70) | DUPLICATE / tombstone | 20 requests to an endpoint that is permanently `410 Gone` (query_table.go:247); took 1.66 s. Already checked by the loop at query_table_exploration_test.go:55–59. | Keep one 410 + one 401 request inside the merged dataset smoke |
| TestLiveClaudeUsageAttributionSample (usage_attribution_live_test.go:15) | PERF probe | One-off investigation of one named workspace; only logs | None needed |
| TestVendoredV3UngroupedComputedErrorCells (query_table_v3_adapter_review_test.go:38) | DUPLICATE | Same body as upstream `backends/sqlite-tests/query_v3_review_test.go:18` | Upstream suite run against the fork (§3) |
| TestVendoredV3CalendarQueryRangeIgnoresClearedFilters (:166) | DUPLICATE | = upstream `calendar_review_test.go:14` | ″ |
| TestVendoredV3CalendarShownRowsPreservesHiddenSort (:183) | DUPLICATE | = upstream `calendar_review_test.go:162` | ″ |
| TestVendoredV3FormulaGroupErrorIdentities (:70), TestVendoredV3HiddenAndDefaultSort (:118) | DUPLICATE, but they hit **patched** code (fused group summaries, materialized representatives) | = upstream `query_v3_review_test.go:177` and the `query_v3_contract_test.go` DefaultSort/SQLiteSortField section | Delete **only after** the fork gate exists. Until then keep them on a `sql.Open(t.TempDir())` DB. |
| TestQueryV2MetricMatrix (query_table_v2_test.go:78), TestQueryV2ComputedAndLimits (:149) | DUPLICATE | Exercise the qt library on a synthetic `samples` table; covered upstream by `v2_test.go` (TestV2Formulas, PairedGroupLimitScope, ComputedRowsAndRevisionSnapshot, ValidationAndBudgets). Cheap (0.07 s), so this is a maintenance win, not a speed win. | Upstream suite against the fork |
| `referenceSearchConversations` + its use in TestMCPConversationSearchMatchesItsFullScan (mcp_search_test.go:16–163, :165) | CHANGE-DETECTOR | Freezes the pre-FTS5-ranking algorithm, so every intentional ranking change means editing two implementations | Explicit expected `conversation_id` orderings per query (the fixture has 5 conversations). Keep the passage half as is. |
| third_party/query-table-go/*_test.go (19 files) | DUPLICATE | Never run from the root; identical to v0.8.0; 2 fail by construction (missing `../../schema`) | §3 fork gate. Update README.pharos.md, which says "Upstream tests are included". |

### Move behind `//go:build probe` (or into `tools/`)

They cost nothing when skipped at runtime, but they compile into every agent build, and some never terminate.

- TestMCPLatencyReport and its generators, mcp_latency_test.go:26–235. The package-level `benchWords` (:153) runs at every binary start.
- TestQueryPerformanceProbe / TestQueryPerformanceProfile / TestQueryIndexedPerformanceProfile, performance_probe_test.go:17/68/96.
- TestUsagePerformancePreparation / Group / MachineTime / Server, usage_performance_test.go:38/62/86/107. `Server` blocks in `ListenAndServe` forever.
- TestTextSearchPreview (text_search_preview_test.go:12): a 2-minute sleep.
- TestTextSearchReplica (text_search_replica_test.go:18): wall-clock thresholds against a real replica.

### Shrink (biggest measured wins first)

| test | measured | change | coverage kept by |
|---|---|---|---|
| TestQueryV3LargeSourceHavingMetricsAndPaging (query_table_v3_test.go:71) | 13.8 s | 10,031 rows go through `seedToolCalls`, which calls `tx.Exec` for every 40-column row. Option 1: inject the V3 row budget (e.g. MaxRows=100) and seed 131. Option 2: bulk-insert through a recursive CTE. | Same "source above row budget" property |
| TestStatisticsSelectiveTextPrecedesBothLexicalCuts (mcp_statistics_test.go:290) | 11.2 s | Turn `LIMIT 25000` (mcp_statistics.go:523, also `retrieval_limits` :749 and search_explain.go:19) into a package var. Set it to 50 and insert 51 noise rows. | Same cut-ordering assertion |
| TestQuerySnapshotQuotaPreservesExistingLinks (query_table_exploration_test.go:129) | 5.8 s | `const querySnapshotCount = 10_000` (query_table_exploration.go:87) → var, set to 5 in the test | Same 507/200 assertions |
| TestToolCallCubeAnswersLikeTheCalls (tool_cube_test.go:137) | n/r (heaviest seed) | Seed 3,000 → ~500 (keep `len(fromCube) >= 100`, lowering it if needed). Run the "full" op matrix (`index%6==0`, about 430 aggregations) for 1–2 wheres by default and everything under `PHAROS_SLOW_TESTS=1`. | Same differential oracle |
| TestToolCommandSearchIndex (tool_search_test.go:11) | n/r | Seed 650 exists only to exceed one backfill batch. Make the batch size a var and seed about 30. | Same triggers/backfill coverage |
| TestQueryV2IndexedCandidates (query_table_v2_test.go:378) | 1.5 s | Seed 650 → ~100 (it does not depend on the batch size) | – |
| TestQueryV2PharosHTTP (:200), TestQueryMetricPopulationsHTTP (query_table_metric_population_test.go:28), TestSharedExportScopesTablesToItsWorks (share_test.go:137) | 1.2–1.4 s each | Seed 300 → 40–100 (adjust `MinCount: 70`) | – |
| TestMCPWorkDetailSectionsAndFieldContinuation (mcp_provenance_test.go:13) | 0.8 s | Repeat 60,000 → about 3,000 (still several chunks) | Unicode continuation property |
| TestTextSearchTimeoutRequiresProgress (text_search_cursor_test.go:73) | killed in the run | Set `TimeoutMS` 500 → 150 (:86). Use the template fixture for the 6 subtests. Verify that the `first_row` phase cannot hang: it was 3 s into a 500 ms budget when the timeout fired. | – |
| `seedToolCalls` helper (tool_cube_test.go:17), used by about 20 tests | – | `tx.Prepare` once instead of 40-arg `tx.Exec` per row | – |

### Merge / table-drive

- **Per-dataset HTTP smoke**: merge TestQueryExplorationHTTP (query_table_exploration_test.go:26), TestQueryV2PharosHTTP (query_table_v2_test.go:200) and TestQueryV3AllDatasetsHTTP (query_table_v3_test.go:26). All three loop over the same 10 datasets with their own fixture. Use one fixture (seed 31 + usage fixture) and run per dataset: capabilities (V2 computedExecution + V3 caps/budgets), rows-v2, item, metrics, query-v3. This saves 2 fixtures (about 3–6 s measured) and about 120 lines.
- **Plan/index guards** (CHANGE-DETECTOR by design, cheap): merge model_effort_test.go:280, query_cache_generation_test.go:177, the plan half of query_rows_indexed_test.go:154, query_table_v2_test.go:378 / :426 / :658 (plan parts), tool_search_test.go:65 and usage_summary_test.go:143 into one `TestQueryPlansUseIndexes{query, wantSubstring}` on one catalog. Keep them as cheap perf guards. Expect churn on modernc/SQLite upgrades, and say so in the file. The trigram "VIRTUAL TABLE INDEX" check is currently duplicated (tool_search_test.go:71 and query_table_v2_test.go:421).
- **TempStorageLease V2/V3**: query_table_v2_test.go:618 + query_table_v3_test.go:265 → one table over `{beginQueryV2, mode 2}` and `{beginQueryV3, mode 1}`.
- **Metadata queue**: query_table_v2_test.go:709 + query_table_v3_test.go:297 → same file and fixture.
- **Hourly-usage semantics across engines**: usage_table_test.go:178 (legacy `/aggregations`), query_table_v2_test.go:335 (V2 metrics), :534 (shownRows) and query_table_metric_population_test.go:132 (metric WHERE). All of them set `time.Local=UTC` and ingest the same literal `r3` event, copied 4 times. Use one `hourlyUsageCatalog(t)` and one table of `{engine, request, want}`.
- **Indexed page ≡ plain executor**: query_rows_indexed_test.go:116 + usage_query_performance_test.go:315 → one table, both on seeded tool_calls.
- **Freshness mutations**: TestMachineTimeTimestampRangesRequireFreshCube (usage_query_performance_test.go:172) builds 5 catalogs. Use 1 catalog and re-run `ensureToolRollup` (or restore `meta`) between mutations.
- **Repository merge planner**: repository_identity_test.go:41, 77, 112, 471, 506, 524 and 731 are pure `planRepositoryMerges` scenarios → one case table `{rows, aliases, wantGroups func}`. The two git tests (:187, :577) share an identical `git init` + `remote add` fixture → one test with subtests.
- **Dual-engine conformance**: sql_table_test.go:12/57/84/129 are twins of internal/querytable/apply_test.go:108/47/215/265, with the same cases and expected IDs against two engines. Move the case tables (rows, where, want) into an exported `internal/querytable/querytabletest` package and run them through `querytable.Apply` and `sqlDataset.Rows`. That guarantees parity instead of hoping two copies stay in sync.
- text_search_review_test.go → text_search_test.go. The 17-paren nesting rejection appears in both text_search_review_test.go:103 and text_search_test.go:251.
- query_table_v2_review_test.go → query_table_v2_test.go. origin_test.go: 2 fixtures → 1.

### Gate (opt-in or `-short`)

Leave the durability and integration tests in the default run. Add a documented fast lane: `go test -short -vet=off ./internal/archive -run …`. Skip under `-short`:
- the full matrix of TestToolCallCubeAnswersLikeTheCalls;
- the capture-backed variants of TestNamingAuditFreezesMetadata (naming_audit_test.go:45);
- TestInitializeRunsOneAtATime (python3 + 300 ms wait, mcp_library_test.go:759);
- TestMCPHoldsNoCatalogHandleBetweenCalls (8× `lsof`, mcp_library_test.go:867).

TestMCPRestartKeepsTheConnection and TestServeStopsCleanlyOnSIGTERM are already `-short`-gated. Agents rarely pass `-short`, so this needs to be documented in AGENTS/CLAUDE.md.

### Change-detector fixes (keep the test, change the assertion)

- message_search_filter_test.go:59: `strings.Contains(table.base, "0=1")` → assert the dataset returns zero rows.
- query_rows_indexed_test.go:158–164: `d.from` contains table names. This is a join-pruning check on SQL text; move it to the plan table or assert on the EXPLAIN.
- query_cache_generation_test.go:192: EXPLAIN text. The real intent is "index re-created on Initialize", so check `sqlite_master`.
- writing_messages_test.go:134 `TestSpanRulesMatchTheSchema`: the reason list (:165) is hand-maintained, so new production reasons are never checked. Enumerate them from the production rule table.

---

## 3. Harness duplication and consolidation proposal

**Duplication found in this scope.**
- **8 HTTP-call helpers** with the same body: `serveTest`, `preferencesCall`, `probeCall`, `queryExplorationCall`, `queryV2ReviewCall`, `libraryStatusCall`, `queryLibrary`, plus about 15 inline `call := func(...)` / `get := func(...)` closures (tl1_test.go:562, work_reader_test.go:242, query_table_v2_test.go:204, query_table_metric_population_test.go:32, text_search_review_test.go:217, …).
- **3 MCP-call helpers**: `auditCall`, `statisticsCall`, and inline `list` (mcp_repositories_test.go:33).
- Two tool-text extractors: `toolText` (mcp_library_test.go:143) and `toolTextOf` (mcp_latency_test.go:140).
- **Ingest boilerplate**: about 14 inline `Begin` / `ingestWorkspace` / `Rollback` / `Commit` blocks in scope, besides the helpers `ingestRecords` and `ingestNamingRecord` that do the same thing. 27 `MakeAdapter(SourceConfig{…})` + `Ingest` sequences package-wide (`ingestUsageFixture`, `ingestMCPFixture`, `ingestTL1Registry`, `ingestFixture`, …).
- **13 `querySchemaDocument` + `querytable.LoadSchema` pairs**, including two local `load := func` copies (skill_usage_test.go:204, tool_ledger_test.go:191).
- **Fixture builders that are pure SQL inserts and fully deterministic**: `libraryFixture` (40 uses package-wide), `auditFixture` / `provenanceFixture`, `statisticsFixture`, `textFixture`, `modelEffortFixture`, `seedToolCalls(n)`, `ingestUsageFixture` (26 uses).
- **Ten tests in repository_identity_test.go call `OpenCatalog(filepath.Join(t.TempDir(), …))` directly**, a second spelling of `testCatalog`.

**Proposal: a single `harness_test.go`.**

1. **Pre-built templates, copied per test.** Use `seededCatalog(t, name)` with a `sync.Once` per template: `"empty"`, `"library"`, `"audit"`, `"provenance"`, `"statistics"`, `"text"`, `"usage"`, `"usage-hourly"`, `"tools-31"`, `"tools-131"`, `"tl1"`, `"mcp-library"` (a whole library dir).
   - Build each template once in a process temp dir, then `Close()` it (TRUNCATE checkpoint).
   - Per test, copy `catalog.sqlite3` into `t.TempDir()` (a few hundred KB) and open it with a test-only `openCatalogForTest`. That function skips `Initialize` when `initializedByThisBuild()` is true, the same check `OpenCatalogForQuery` already uses (catalog_query.go:101).
   - Migration tests keep calling `catalog.Initialize()` explicitly; many already do (pricing_test.go:215, timestamps_test.go:210, token_usage_test.go:176, usage_table_test.go:88, mcp_library_test.go:646, …).
   - Expected effect: the per-test floor drops from about 0.67 s (median, measured) to file-copy cost. On the 161 fixture tests that ran in scope, that is on the order of 100 s of the 189 s measured.
2. **Test-mode durability pragmas.** Add a package var consulted by `catalogDSN` (durability.go:39). TestMain sets it to `synchronous(OFF)` (or NORMAL) and `checkpoint_fullfsync(0)`. `durability_test.go` (another reviewer's scope) restores the production values for its own tests. Its `init()` already flips `commitFullFsync`, so the pattern exists. Evidence that this matters: 29% CPU utilization over the 607 s run.
3. **Hermetic TestMain additions.**
   - Set `PHAROS_HOST_ID` / `PHAROS_HOST_LABEL`. Without them, `currentHost()` (hosts.go:41) shells out to `scutil` and to `ioreg` with 5 s/15 s waits, once per process including each re-exec'd helper. Note: `scutil` still runs unless `detectHost` checks the env var first, because Go evaluates `defaultString` arguments eagerly (hosts.go:67).
   - Set `GIT_CONFIG_GLOBAL=/dev/null` and `GIT_CONFIG_NOSYSTEM=1` so real-git tests ignore the user's signing and hooks config.
4. **One HTTP helper**: `apiCall(t, server, method, path string, body any, opt ...callOpt) *httptest.ResponseRecorder` plus `apiJSON[T any](…) (int, T)`, with options for no token, Origin and Sec-Fetch-Site. **One MCP helper**: `mcpCall(t, c, tool, args) map[string]any` / `mcpErr`, plus one `toolText`. **One ingest helper**: `ingest(t, c, records...)` and `ingestExport(t, c, kind, files map[string]string)`. **One schema helper**: `loadQuerySchema(t, name)`. Use `same` (tool_cube_test.go:91) as the shared tolerant comparator.
5. **One upstream-patch gate**, replacing the 19 third_party test copies and the 5 `TestVendoredV3*` copies. Add `tools/test-query-table-fork.sh`. It runs the owner's upstream `backends/sqlite-tests` (and `backends/go`) at tag v0.8.0 with a temporary `go.work` / `replace` pointing at `third_party/query-table-go`. That is the only suite that exercises V3 grouped and fused summaries and staging against the patched code; the published module zip has **no** V3 tests at all.
   - In-repo host guards for the patches today: `TestUsageFusedSummariesMatchIndependentMetrics` (usage_query_performance_test.go:281); `TestQueryV3AllDatasetsHTTP`, which asserts the 100k usage group budget (query_table_v3_test.go:60–66); and `TestQueryV2ReductionReuse` / `TestQueryV2IndexedWindow` (materialization).
   - Gaps: no host test covers the compiler-limit fallback or the "sampled summaries keep their original path" clause in README.pharos.md.

**Why `t.Parallel` is not the first lever.** Tests mutate package globals: `currentHost` via `useHost`, `time.Local`, `parseHook`, `syncAdapter`, `probeShellEnv`, `probeTempDirs`, `commitFullFsync`, plus `t.Setenv` and `t.Chdir`. That happens in 14 in-scope files and 30 files package-wide. Make those injectable before parallelizing.

---

## 4. Splitting `internal/archive` for compile and test time

- **The compile/link cost is structural.** A 65k-LOC package plus 34.5k LOC of tests compiles as one unit, and every test binary links modernc SQLite. `go test -run X` therefore pays a full package compile, a vet pass and a link after any edit. The deletions above remove only about 1.7k test lines (about 5%), so they barely move compile time. **The split is what moves compile time.** A cheap workflow win in the meantime: `-vet=off` for inner loops.
- **Most separable first: a leaf "transcript model + parsers" package with no SQLite.**
  - Contents: the types `MessageRecord` / `ConversationRecord` / `WorkspaceRecord` (adapters.go:18–60), util.go helpers (`firstString`, `integer`, `jsonText`, `stableID`), shell_command.go, the pure parts of tool_ledger.go (0 `*Catalog` references), tool_urls.go, tool_errors.go, skill_usage.go, message_origin.go, token_usage.go counters, `canonicalTime`, tl1_errors.go, `modelEffort` / `modelFamily`, semantic embed/explain, highlight/snippet segmentation, and the pure `planRepositoryMerges` (repository_identity.go has 6 DB-touching references to split off).
  - Test cluster that maps here: **73 in-scope tests need no catalog**, including tool_ledger ×9, repository_identity ×11, skill_usage ×5, message_origin ×4, tl1 ×4, search_explain ×3, token_usage ×2 and tool_urls ×2. In a package that does not import SQLite, they would build and run in about a second.
  - This is the code agents edit most (parsers and ledgers) and gets the most benefit from fast loops.
- **Subsystem clusters and their cycle risk.**
  - (a) **Query tables**: query_table*.go, sql_table.go, query_rows_indexed, query_cache_generation, tool_cube, tool_search, usage_query_paths. This is the largest test cluster in scope, roughly 60 tests and about 70 s measured. Its datasets reach into `Catalog` caches (`c.library.rows`, `c.derived`, the find cache), so it needs a `catalog` package that `datasets` imports, with `Server` (api.go) above both. Medium cycle risk.
  - (b) **Text search** (text_search*.go, message search filter, search_explain): it reads SQL tables and meta generations and is called by Library and MCP. It could take a `*sql.DB` plus a small interface. Low–medium risk.
  - (c) **MCP** (mcp_*.go): the top consumer. It calls search, work detail and statistics, and the `/api/mcp/rpc` handler in Server calls `handleMCP`. Keep it one-way: Server imports mcp, and mcp never imports the Server type. Tests currently build `NewServer` for forwarding, which would need to move to an integration test package. Medium risk.
  - (d) **TL1 adapter + analytics**: the adapter can sit above the leaf model package; the overview queries need the catalog. Low risk once the Adapter interface lives in the leaf.
  - (e) **Config / library / probe / service shell**: depends on everything. Stays in the top package.
- **Order with the lowest cycle risk**:
  1. Move util helpers and record types to a leaf (mechanical).
  2. Move pure parsers.
  3. Introduce `catalog` (Catalog, schema, Initialize, durability) and have the rest import it.
  4. Datasets and textsearch.
  5. MCP.

  The main cycle hazards are package-level hook variables used by tests and production alike: `currentHost`, `parseHook`, `syncAdapter`, `githubCLI`. These would need to become fields or interfaces on the struct that owns them.

---

## 5. Surprises

- **The default `go test ./...` fails today on this machine** (10-minute timeout, `go-test.json`), so about 113 in-scope tests, alphabetically after text_search_cursor, did not run in the benchmark run.
- **Run-time is mostly waiting**: user+sys 174 s versus 607 s real. The catalog's own logger shows `query-cache-schema` held the writer for 8.6 s and 6.6 s during ordinary fixture opens.
- **Tests that cannot fail or never run**:
  - Probes that only `t.Log`: performance_probe ×3, usage_performance ×3, mcp_latency, usage_attribution_live, text_search_replica.
  - `TestUsagePerformanceServer` can only end in `t.Fatal(ListenAndServe)`.
  - `TestTextSearchPreview` sleeps 2 minutes.
  - `TestProtectStoresCanonicalTimestamp` requires `-tags reclamation`.
  - The 124 third_party tests never run, and 2 of them would fail on missing fixtures.
  - `TestSpanRulesMatchTheSchema` cannot detect new rules.
  - The negative-window checks (`time.After(25ms/30ms/50ms/300ms)`, e.g. query_cache_generation_test.go:163, query_table_v3_test.go:321, service_test.go:185) can only falsely pass.
- **Wall-clock thresholds that can flake under parallel agent load**:
  - mcp_library_test.go:292 (>2 s, with a 5 s sleeping host stub that keeps a goroutine sleeping after the test), :703 (>1 s), :806 (>5 s);
  - probe_test.go:261 (>2 s) and :267 (>1.5 s), real shells leaving `sleep 30`/`sleep 5` background jobs;
  - text_search_cursor_test.go:86 (500 ms budget, plus the suspected hang);
  - text_search_replica (2.5/3 s, opt-in).
- **Real system and home directory**:
  - `currentHost()` runs `scutil` + `ioreg` on the real machine.
  - `TestRepositoryFromWorktree` and the two git tests in repository_identity use the user's global git config.
  - `TestInitializeRunsOneAtATime` needs `/usr/bin/python3`, which on a Mac without the Command Line Tools is an install-prompt shim.
  - `holdsCatalog` runs `lsof` about 8 times per test.
  - Two tests re-exec the test binary (`TestMCPHelper`, `TestServeHelper`), and each child re-runs TestMain plus host detection.
  - TestNewPerUserInstallKeepsDataInApplicationSupport and the config `~` expansion read the real `$HOME` (read-only).
  - No external network: GitHub is stubbed in TestMain and everything else is loopback. `closedPort` (bind-then-close) has a benign reuse race.
- **Misleading names**:
  - usage_query_performance_test.go has no performance assertions.
  - sqlite_pharos_performance_test.go in third_party is upstream's file, not a Pharos patch test.
  - `*_review_test.go` files are ordinary behavior tests, except query_table_v3_adapter_review_test.go, whose tests are upstream copies.
  - `TestMCPListsToolsWithoutWaitingForTheHost` loops over a one-element slice (mcp_library_test.go:281).
  - `probeCall` calls `t.Fatalf` from goroutines in source_toggle_test.go:31/49 (not allowed by `testing`).
- **Three production query engines are tested in parallel**: in-memory `querytable.Apply`, legacy `sqlDataset` SQL (still used by the UI: `/api/query/*/aggregations` and `/api/query/library/aggregations` in query-tables.js and ui.py), and qt V2/V3. This is why so many parity tests exist, and they are legitimate while all three ship. Retiring the legacy `/aggregations` path in the UI would let about 15 parity and legacy tests go.

### Rough class counts (in-scope archive `Test*` = 331)

| class | count |
|---|---|
| BEHAVIOR | ≈277 (includes ≈56 pure unit tests) |
| INTEGRATION-ESSENTIAL | ≈25 (MCP process lifecycle, service SIGTERM/release, naming audit, real git, initialize lock), plus 2 helper processes |
| CHANGE-DETECTOR | ≈7 (EXPLAIN index-name guards ×5, legacy search oracle, span-rule list) |
| DUPLICATE | 8 (5 TestVendoredV3*, 2 V2 upstream copies, the population tombstone), plus 2 upstream-duplicate patch guards to keep |
| PERF/BENCH probes | 11 (+4 Benchmarks, fine as is) |
| SUPERSEDED | 1 (reclamation) |

`internal/querytable`: 16 BEHAVIOR (4 parity twins to merge). `third_party`: 124 DUPLICATE, 0 run, 0 patch guards.
