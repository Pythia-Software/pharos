> Working notes behind [the test suite survey](../testing-survey.md), written by one of the five 2026-10-19 reviews at `bb496be`. Line numbers refer to that commit. Classifications are judgement calls; the timings here are indicative, taken from contended runs or the Pharos archive. See the survey for measured numbers.

# Go test audit: internal/archive, activity_progress_test.go … mcp_history_test.go (58 files)

Scope: `ls internal/archive/*_test.go | sed -n '1,58p'` — 58 files, 18,446 lines, 396 test/benchmark funcs (+TestMain).
Method: read every file; `git log` for provenance; grep for product callers. No tests were run.

**Timings** below are *indicative only*: they come from the already-present `.context/survey/go-test.json`
(another agent's full run, possibly contended). That run **FAILED at the default 10 min `go test` timeout**
(`panic: test timed out after 10m0s` while in `TestTextSearchTimeoutRequiresProgress`, out of scope — it had
been running 7 s; the suite simply ran out of budget). Top-level tests summed to 593 s; **this scope accounts for
404 s (68%)**.

Class codes: **B** BEHAVIOR, **C** CHANGE-DETECTOR, **D** DUPLICATE, **P** PERF/BENCH (incl. env-gated tools and
deterministic efficiency guards), **I** INTEGRATION-ESSENTIAL, **S** SUPERSEDED.

Totals (all 396): **B 287 · I 77 · P 16 · D 9 · C 4 · S 3**. Indicative seconds by class: B 274, I 109, P 13, D 4.6, C 2.3, S 1.1.
Headline: the tests are overwhelmingly legitimate; **the time goes to harness costs, not to low-value tests.**
Deleting every D/C/S test saves ~8 s. Fixing catalog creation and autocommit seeding saves an estimated 130–190 s.

---

## 0. The three dominant cost drivers (scope-wide)

1. **Every `testCatalog`/`OpenCatalog` costs ~0.3–0.7 s.** `OpenCatalog → initializeLocked(true) → Initialize()`
   always runs: 1,232-line `schema.sql` (225 statements), ~60 `hasColumn` migration checks, `ensureAnalysisPartitions`,
   `ensureTL1Schema`, `ensureFindingsSchema`, `migrateHosts`, `ensureLibrary`, etc., each statement autocommitted under
   `synchronous=FULL`. Evidence: tests that are nothing but `testCatalog`+one query take 0.36–0.74 s
   (`TestCatalogPoolEnforcesForeignKeys` 0.44, `TestNativeAliasLookupUsesIndexedAccountAndNativeID` 0.74,
   `TestMCPReadableMessageWindows` 0.31). In scope: 170 `testCatalog(t)` + 19 `OpenCatalog(` + fixture wrappers
   (libraryFixture 24, newFindingFixture 21, automaticTestServer 28, backupFixture 17, newLegacyInstall 11, auditFixture 9 …)
   multiplied by table subtests → **≈300+ catalog initializations ≈ 100–150 s**.
2. **Autocommit-per-row fixture seeding.** Helpers call `catalog.DB.Exec` per row, so every row is its own
   fsync'd transaction (~3 ms each observed):
   - `TestHeavyOutputAndOutlierDetectors` 13.4 s — `findingFixture.addConversation` inserts 7×1,200 `model_requests` one by one.
   - `TestMCPSearchMessagesExhaustivePagination` 8.3 s — `addAuditMessage` × 1,240 = 2,480 autocommits.
   - `TestVerifyAllEndpointChecksEveryInputIncludingLargeConductorSession` 13.3 s — `execSQL` runs 2,003 conductor inserts one statement at a time, then a 2.3 s ingest.
   - every findings test: `conversation()`/`addConversation()` ≈ 5–10 autocommits × 28 days.
3. **Hard-coded product constants force big fixtures.** `conversation_search.go:237 options.Limit = 5000` (→ 5,100-row
   fixture), `automaticAuditBytes = 16<<20` const (→ 18 MB log), conductor audit gate `unit.Bytes > 2000`
   (→ 2,001-message session), `checkpointWaitMS = 1000` const (→ ~2 s fixed waits), `maxSQLBuckets = 2000` (→ 2,100 calls).
   Turning these into package vars (like the existing `backupStepPages`, `adoptCopyChunk`, `identityTimeout` seams) lets
   tests use ~10 rows.

---

## 1. Per-file table

Secs = indicative sum of top-level tests from go-test.json. Conf = confidence in the recommendation.

| file | tests | lines | secs | cost drivers | dominant class | recommendation | conf |
|---|---|---|---|---|---|---|---|
| activity_progress_test.go | 10 | 340 | 1.4 | 3 catalogs; one real capture+index with blocking hooks | B | keep | high |
| activity_timing_test.go | 2 | 89 | 0.5 | 2 catalogs | S | **delete both** (+ dead `activityDuration` read path, see §2) | high |
| adapter_antigravity_test.go | 6 | 473 | 1.9 | protobuf/SQLite builders; 2 tests capture then index into a 2nd catalog | B (1 I) | keep; build `antigravityFixture` once per process and copy | high |
| adapter_titles_test.go | 5 | 256 | 1.9 | 3 tests do ingest→capture→index on 2nd catalog (6 catalogs) | B | keep; extract a capture-round-trip helper | med |
| add_this_mac_test.go | 1 | 75 | 0.6 | InitLibrary + capture + index | B | keep (only CLI onboarding E2E); output-substring checks are mild C | med |
| adopt_test.go | 12 | 958 | 10.7 | `newLegacyInstall` ×11 (12 JSONL files, OpenCatalog, Ingest, rewind to schema 4); AdoptLibrary copy+migrate; 8-stage failure loop; Gosched spin writer | I | keep (documented `--adopt` path, data-loss guards); build legacy install once + copy tree; candidate for `-short` skip (adopt.go unchanged since 2026-09-28) | med-high |
| analysis_partitions_test.go | 14 | 589 | 10.5 | incremental-vs-reference parity: seedToolCalls(1200/400/200) + full rebuild per step; 28-day finding fixtures + reference pass per step | B (3 P) | keep (differential oracles for derived tables); shrink seeds (1200→~200); share month fixture | med |
| analysis_replica_test.go | 1 | 185 | skip | env-gated profiling tool; hard-coded `/Volumes/euclid` guard | P | move behind `//go:build tools` or into `cmd/` so it isn't compiled into every test binary | high |
| archive_test.go | 29 | 1137 | 8.0 | 21 catalogs; 7 inline "ingestWorkspace in a tx" copies; 17 MB JSONL line (cheap, 0.06 s) | B | delete 2 duplicates; move TestMain/testCatalog to harness; split parser tests vs catalog/API tests | high |
| authorship_dump_test.go | 1 | 47 | tag `prototype` | none | D | delete (same tool as authorship_eval) or move to cmd | high |
| authorship_eval_test.go | 1 | 55 | skip | env-gated offline tool | P | move to tools tag/cmd | high |
| authorship_feedback_test.go | 9 | 222 | 0.4 | pure classifier; 1 catalog + redundant `Initialize()` | B | keep; drop redundant Initialize | high |
| authorship_test.go | 19 | 546 | 3.3 | 7 catalogs; 3 redundant `Initialize()` | B | keep; fix/delete TestProseIndexServesAuthorshipQueries | med |
| backup_test.go | 16 | 699 | 16.7 | `backupFixture` ×17 (InitLibrary+OpenCatalog+files); 32 MB filler + concurrent writer | I | keep; shrink WAL-bound test; template the fixture | med |
| capture_bench_test.go | 1 | 72 | skip | reads real `~/.claude`, `~/.codex`… when env set | P | move to tools tag | high |
| capture_index_guard_test.go | 10 | 361 | 13.8 | 3 tables × 3 subtests each = catalog + capture + ingest + index; 1 real-git test | I | keep (rollback/data-loss guards); absorb the plain "older capture" case; merge git test | high |
| capture_index_test.go | 22 | 1101 | 12.1 | 20 catalogs; ~35 capture/index passes; 2 real-git tests; 3 polling loops | B/I | keep; merge git tests and resume/count tests (§2) | med |
| capture_iopolicy_darwin_test.go | 1 | 25 | 0 | none | B | keep | high |
| capture_sqlite_test.go | 1 | 114 | 0.6 | 2,000 commits + one-page-step snapshot under concurrent writer | I | keep as *the* snapshot-consistency guard; ~300 rows suffice | med |
| capture_test.go | 12 | 729 | 5.9 | 5,000×4 KB commits (20 MB) + concurrent writer in one test; ~25 runCapture | I | shrink TestCaptureSnapshotsSQLiteConsistentlyWhileWritten (§2); keep rest | med |
| carbon_test.go | 7 | 276 | 2.9 | 4 catalogs; 12 HTTP calls | B | keep | high |
| catalog_contention_test.go | 2 | 86 | 1.5 | 100 ms sleep | B | keep | high |
| conversation_activity_test.go | 1 | 68 | 0.5 | 1 catalog | B | keep | high |
| conversation_search_test.go | 2 | 96 | 1.3 | 10,050-term vocabulary row | B | keep | high |
| delegation_cache_test.go | 5 | 259 | 3.3 | `libraryFixture` ×5 though only a commit is needed | B | keep; use bare catalog | med |
| delegation_links_test.go | 4 | 181 | 5.0 | catalog+seed per subtest (3+4 subtests) | B | share one catalog per table (reset rows) or template | med |
| delegation_test.go | 15 | 570 | 17.5 | ~39 catalogs: TestDelegationAssignmentLabels alone = 15 catalogs (8.7 s) for a json_each label rule | B | table rows share one seeded catalog (§2) | high |
| derived_cache_test.go | 2 | 110 | 1.2 | 1–10 ms sleeps | B | keep | high |
| devui_test.go | 3 | 162 | 0 | httptest | B | keep | high |
| drive_test.go | 5 | 323 | 1.5 | `quietDriveCache` waits (≤30 s) for real diskutil refreshes leaked by *other* tests' `/api/health` calls | B | keep; stub driveTool globally in TestMain (§3) | high |
| durability_test.go | 7 | 281 | 7.5 | ~2 s fixed waits (const `checkpointWaitMS`=1000 hit twice); WAL polling; `init(){commitFullFsync=false}` | I | keep; make checkpointWaitMS a var; move the `init` knob into TestMain | high |
| effort_test.go | 10 | 472 | 3.3 | one cross-surface test (v1 aggregate, V2 metrics, MCP, library, share) | B | keep; rewrite index test to product SQL only | med |
| findings_detectors_test.go | 5 | 239 | 18.2 | 28-day fixtures inserted row-by-row; 8,400 model_requests autocommits in one test | B | **batch fixture inserts in one tx** (biggest single win in findings) | high |
| findings_estimate_test.go | 5 | 105 | 0 | pure | B | keep | high |
| findings_review_test.go | 7 | 236 | 4.9 | 28-day fixture + full RefreshFindings per test | B | share a pre-built finding catalog (copy per test) | med |
| findings_test.go | 9 | 411 | 2.2 | lifecycle E2E (incl. MCP list/get_finding) | B | keep; `newFindingFixture` should use testCatalog | high |
| findings_verify_test.go | 8 | 274 | 8.1 | `copiedFinding` ×5 (28 days + refresh + cart copy) | B | prebuild "copied finding" state once, copy file | med |
| git_integration_test.go | 2 | 187 | 4.1 | ~30 `git` subprocesses; PATH shim script | I | keep (only guard of merge attribution via real git); share repo builder | med |
| harness_version_test.go | 7 | 235 | 3.8 | 4 catalogs | B | keep | high |
| health_test.go | 2 | 90 | 2.2 | libraryFixture + otherProcess | B | keep | high |
| home_query_recipes_test.go | 2 | 154 | 5.3 | 256 HTTP query executions over `docs/homepage-query-cards.json` | B | keep (contract for shipped recipes); read-only subtests are the best `t.Parallel` candidate | high |
| hosts_test.go | 8 | 492 | 8.8 | in-memory `copyFixture` (cheap); real `/usr/sbin/ioreg` in one test | I/B | keep; promote `copyFixture` as the standard fake source | high |
| incremental_bench_test.go | 1+1 bench | 176 | skip | env-gated resource budgets | P | move to tools tag | high |
| incremental_recovery_regression_test.go | 9 | 282 | 13.9 | captures, recovery-job polling, automaticTestServer | B/I | dissolve into subsystem files; merge 1 D, rewrite/delete 1 C (§2) | high |
| incremental_review_regression_test.go | 12 | 408 | 23.0 | 2,003 autocommit conductor rows + 2.3 s ingest; 18 MB log; polling | B/I | dissolve into subsystem files; delete 1 S, merge 2 D; batch/var the big fixtures | high |
| incremental_sync_test.go | 22 | 770 | 62.6 | 28 automaticTestServer; `runManualSync` full pipeline (github/identities/git/tools/authorship/findings); `runAutomatic` with audits | B/I | keep; template catalogs; `-short` gate for full-pipeline runs; merge 1 D | med |
| incremental_writer_test.go | 3+1 bench | 179 | 16.7 | parity test = 16 catalogs; 2,000-message ingests | B/P | parity: one catalog pair with distinct SourceIDs; keep row budget | high |
| instructions_test.go | 4 | 291 | 4.1 | `writeLines` duplicates `writeSourceFile` | B | keep | high |
| launches_test.go | 2 | 226 | 1.2 | redundant `Initialize()` | B | keep | high |
| library_cache_test.go | 4 | 210 | 5.7 | 8×10 concurrent HTTP | B | keep | high |
| library_find_filter_test.go | 2 | 120 | 7.7 | 5,100-row insert to exceed hard-coded 5,000 cap | B | make cap a var → ~10 rows | med |
| library_status_test.go | 7 | 364 | 4.1 | polling; real volume facts for TMPDIR | B | keep; make drive-location assertion host-independent | med |
| library_test.go | 10 | 528 | 10.3 | libraryFixture; v1 query-table differential | B/P | keep; delete 1 D | high |
| library_upgrade_test.go | 9 | 529 | 14.7 | `legacyUpgradeCatalog` ×4; `/bin/sh` gh stubs; polling | I/B | keep (repository-merge migrations); prebuild legacy catalog once | med |
| machine_activity_test.go | 3 | 128 | 3.1 | seedToolCalls(2100) to exceed `maxSQLBuckets` | B | keep | high |
| mcp_audit_test.go | 12 | 580 | 32.2 | 2,480 autocommit inserts; 14 freshness subtests each a new catalog | B | batch inserts; one catalog for the freshness tables; merge duplicated agreement checks | high |
| mcp_conversations_test.go | 1 | 101 | 12.5 (outlier) | 1 catalog, 1 ingest, 6 MCP calls — should be <1 s | B | keep; investigate the outlier (likely leaked background work, §5) | med |
| mcp_history_test.go | 2 | 174 | 1.3 | — | B | keep | high |

---

## 2. Per-test actions

### Delete (S / D / C)
| Test (file:line) | Class | Why | What preserves coverage |
|---|---|---|---|
| TestActivityDurationHistory (activity_timing_test.go:8) | S | `activityDuration()` (median of `activity_durations:*` meta) has **no product caller**; #127 (2026-10-15) replaced completion estimates with the workload-pace estimator in activity_progress.go. The `recordActivityDuration` writes (api.go:949, capture_api.go:226, backup_api.go:131, library_status.go:134) are now write-only. | activity_progress_test.go (estimator). Owner should also delete `activityDuration`/`activityDurations`/`recordActivityDuration` (product change — flag, not done). |
| TestLibraryActivityCompletionEstimate (activity_timing_test.go:50) | S | Same dead path; its "no ETA without a workload" assertion is the estimator's `learning`/`unknown` state. | TestStageEstimate*, TestActivityEstimatesResetWithStageSourceAndRun |
| TestIncrementalActivityEstimatesKeepRunKindsSeparate (incremental_review_regression_test.go:349) | S | Exercises `activityDuration` per run kind (same dead path). | as above |
| TestCatalogPoolEnforcesForeignKeys (archive_test.go:62) | D | Strict subset of TestCatalogPoolAppliesDurabilityPragmas (durability_test.go:42), which checks `foreign_keys=1` (plus 5 more pragmas) on all 4 pooled connections. | durability test |
| TestEmbeddedUIIsServedWithoutPython (archive_test.go:1115) | D | `appHTML()` prefix + "Pharos" substring; "WithoutPython" is historical. TestAppRoutesServeUIOnDirectNavigation already serves `/` and asserts the shell. | routes test |
| TestReopenedCatalogHasNoLegacyQueryStats (library_test.go:278) | D | Same `sqlite_master LIKE 'workspace_query_stats%'` assertion ends TestLibraryProjectionRebuildsWhenDefinitionChanges; costs a full catalog + second Initialize (0.67 s). | library_test.go:251 |
| TestDumpAuthorship (authorship_dump_test.go:16) | D | `prototype`-tagged twin of TestAuthorshipEvalLiveCatalog (both classify a live catalog to JSON). | keep one as a `cmd/` tool |
| TestProseIndexServesAuthorshipQueries (authorship_test.go:308) | C | EXPLAIN on **SQL written in the test**, asserting index name `messages_prose_idx` and no TEMP B-TREE. Cannot catch a product-query regression; breaks on index rename. | Rewrite against the product's authorship query strings (as TestConversationQueriesAvoidProseIndex does with `documentInitiationQuery` etc.) or delete. |
| TestNativeAliasLookupUsesIndexedAccountAndNativeID (incremental_recovery_regression_test.go:129) | C | Same pattern: test-local SQL + exact plan text `SEARCH conversations USING INDEX conversations_native_alias_idx`. | Point at the product's alias-lookup statement or delete. |

Also C (keep, low priority): TestEffortLookupsUseCoveringMessageIndex (effort_test.go:353) — 2 of its 3 queries are test-written copies; keep only the `libraryDerivedSelect` case. TestDelegationDefaultPageSizeIsForty (delegation_test.go:552) pins a default page size.

### Merge (D / partial overlap)
| Tests | Action |
|---|---|
| TestSyncSettingsRouteServesUI (incremental_review_regression_test.go:304) | Add `/settings/sync` to the path list in TestAppRoutesServeUIOnDirectNavigation (archive_test.go:1125); drop its automaticTestServer (1.7 s) and the `id="syncDashboard"` HTML-string checks. |
| TestIndexAsksGitOnlyAboutThisMacsPaths (capture_index_test.go:172) + TestOwnSyncEnrichesWhatAnotherMacIndexed (capture_index_guard_test.go:170) | Both `git init` + `remote add`, capture on host-a, index as host-b → remote must be "", then host-a resolves it. One test, one repo, assert both host-a paths (capture index and own live sync) and the re-index-keeps-enrichment step. |
| TestIndexPassesOverWholeSourceCapturesOlderThanTheLastSync (capture_index_test.go:918) → TestOlderWholeSourceCaptureAfterAFailedOrInterruptedSync (capture_index_guard_test.go:39) | Add "succeeded" and "newer capture wins" rows to the guard's table. |
| TestFullRecaptureBypassesReuseAndResumesVerifiedFiles (incremental_sync_test.go:465) + TestFullRecapturePreservesOnlyChangedFileVersions (incremental_review_regression_test.go:18) | Same stableCodexSource + `CaptureRecoveryID` sequence; one test: force copies, resume doesn't recopy, identical recapture adds no history, changed recapture preserves previous. |
| TestAntigravityUnreadableRequestWALDoesNotAdvanceDiscovery (incremental_recovery_regression_test.go:262) + TestAntigravityDependencyFailureDoesNotAbortHealthySibling (incremental_review_regression_test.go:117) | Identical self-symlinked `-wal` trick; one test asserting: bad group not checked/emitted, sibling emitted, recovery after removal. Move to adapter_antigravity_test.go. |
| TestIndexResumesAfterInterruption (capture_index_test.go:506) + TestIndexWorkCountsWhatRemainsWithoutParsing (capture_index_test.go:541) | Same 5-rollout capture cancelled at 2; fold the resume/coverage assertions into the counting test. |
| TestMCPArchiveStatusSuccessfulNoOpSyncRefreshesLag (mcp_audit_test.go:439, 8 rows) + TestMCPAuditFreshnessAgreesAcrossTools (mcp_audit_test.go:502, 6 rows) | Both assert "search tools' `freshness` == `get_archive_status`". One table on one `auditFixture`, resetting `source_states`/`automatic_source_states` per row: 14 catalog inits → 1 (≈9 s → <1 s). |
| Argument-sanitization checks ("must not be logged"/"secret") in TestMCPRequestedArchiveSourceProjection, TestMCPControlAndCallHistory, TestMCPAuditToolsExposedAndLogged | Keep the `handleMCP`-path check in TestMCPAuditToolsExposedAndLogged; others optional. Low priority (cheap). |
| incremental_review_regression_test.go, incremental_recovery_regression_test.go | Added in the same PR as incremental_sync_test.go (#68, 2026-10-01); the names record review rounds, not a superseded API. After the deletions/merges above, **move the remaining 17 tests by subsystem** (audit/recovery → incremental_sync/audit file; antigravity discovery/log reader → adapter_antigravity_test.go; JSONL discovery race → adapter tests; conductor blocked-snapshot read → conductor/capture tests). Nothing else in them is superseded. |

### Table-drive / share one fixture
| Test | Change | Expected effect |
|---|---|---|
| TestDelegationAssignmentLabels (delegation_test.go:219) | 15 subtests each `testCatalog`+`seedDelegation`. Seed once; per row `UPDATE messages SET text=? WHERE id='launch'` then `delegationSnapshot` (fresh snapshot per catalog version). | 8.7 s → ~0.7 s |
| TestDelegationTopLevelConversationLabels (:158, 5), TestDelegationClaudeTaskReportErrorsPreserveLaunch (delegation_links_test.go:135, 4), TestDelegationCodexLocalHandlesAndAmbiguity (delegation_links_test.go:95, 3) | Same; or template-copy catalogs. | ~5 s → ~1 s |
| TestIncrementalWriterSearchParity (incremental_writer_test.go:54) | 8 scenarios × 2 catalogs. One catalog pair, distinct SourceID/NativeID per scenario (snapshot query filtered by source). | 12 s → ~1.5 s |
| adapter_titles_test.go ×3, adapter_antigravity_test.go ×2 | Shared `assertCaptureRoundTrip(t, config, host, check)` (capture → new catalog → indexCaptures → check). | readability |
| archive_test.go parser tests (TestCodexModelFollowsTurnContext, TestCodexCompactionTrigger, TestCodexMessagesToExistingAgentDoNotCreateSubagents, TestWorkflowToolIsDelegation, TestCodexSubagentMetadataCreatesChildSession…) | Use the existing `parseJSONL(t, provider, path, lines...)` (message_origin_test.go) instead of hand-rolled write+parse; table where assertions are uniform. | readability only (already ~0 s) |

### Shrink
| Test | Change |
|---|---|
| TestHeavyOutputAndOutlierDetectors (findings_detectors_test.go:130, 13.4 s) | Wrap `findingFixture.addConversation`/`conversation` in one transaction (or `INSERT … WITH RECURSIVE` for the 1,200 requests). Benefits all 21 `newFindingFixture` users. |
| TestMCPSearchMessagesExhaustivePagination (mcp_audit_test.go:150, 8.3 s) | `addAuditMessage` in one tx (keep 620 rows). |
| TestVerifyAllEndpointChecksEveryInputIncludingLargeConductorSession (incremental_review_regression_test.go:234, 13.3 s) | `execSQL`/`conductorFixture` batch in one tx; make the conductor audit gate (`unit.Bytes > 2000`, incremental_audit.go:180) a var so "large" can be ~5 messages. |
| TestAntigravityAuditExcludesUnrelatedRunLogEvidence (incremental_review_regression_test.go:315) | 18 MB log only to exceed `const automaticAuditBytes = 16<<20`; make it a var. |
| TestLibraryFindFiltersBeforeGlobalCandidateLimit (library_find_filter_test.go:78, 6.1 s) | 5,100 rows only to exceed `options.Limit = 5000` (conversation_search.go:237); var → ~10 rows. |
| TestCheckpointGivesUpOnAnOldReaderWithoutHoldingUpWriters (durability_test.go:140) | ~2 s of fixed waits from `const checkpointWaitMS = 1000`; var, 50 ms in tests. |
| TestBackupBoundsTheCatalogWAL (backup_test.go:648) | 4,000×8 KB filler (32 MB) + unbounded writer; with `backupStepPages=1` a few hundred pages should suffice. Also timing-sensitive (needs writer to outpace the snapshot). |
| TestCaptureSnapshotsSQLiteConsistentlyWhileWritten (capture_test.go:331, 3.3 s) | 5,000×4 KB commits. Consistency-under-writer is already guarded (with one-page steps) by TestSnapshotSQLiteStepsThroughOneReadTransaction; cut to ~200 rows and keep the capture-specific checks (manifest method, `journal_mode=delete`, cache.db skipped). Its "writer never committed" branch only logs. |
| TestSnapshotSQLiteStepsThroughOneReadTransaction (capture_sqlite_test.go:16) | 2,000→~300 rows (still hundreds of one-page steps). Keep — it is the durability guard. |
| TestIncrementalToolRollupParity (analysis_partitions_test.go:56) | seedToolCalls 1200→~200 (4 workspaces; 6 reference rebuilds). |
| adopt_test.go (11× newLegacyInstall; TestAdoptFailsWhenTheCopyFailsItsCheck builds two + 400 commits) | Build the legacy install once (`sync.Once`) and copy the tree per test, recreating the blob hard link. |
| findings_*: copiedFinding ×6, 28-day `conversation` month ×~12 | Build each distinct fixture once per process, checkpoint, copy the SQLite file per test. |
| Redundant `catalog.Initialize()` right after `testCatalog` | authorship_test.go:237, :310, :334; authorship_feedback_test.go:193; launches_test.go:35 (~0.3 s each). Keep only where re-init is the subject. |

---

## 3. Harness duplication and consolidation

### What is duplicated (in-scope locations)
- **Catalog creation**: `testCatalog` (archive_test.go:40), `newFindingFixture` opens its own (findings_test.go:136), `legacyBackupFixture`/`backupFixture` (backup_test.go:20,77), `newLegacyInstall` (adopt_test.go:30), direct `OpenCatalog` in ~19 places.
- **"ingest a WorkspaceRecord in a tx"** copied inline 11× in scope (archive_test.go ×7, authorship_test, authorship_feedback_test, launches_test, library_upgrade_test) plus `writeIncrementalFixture` (incremental_writer_test.go:19) and out-of-scope `ingestRecords`/`ingestNamingRecord`.
- **Row seeding via raw INSERT loops**, each with its own `run`/`exec` closure and no transaction: libraryFixture, seedDelegation, auditFixture/addAuditMessage, findingFixture.conversation/addConversation, conversation_activity, conversation_search, delegation_links, harness_version, git_integration.
- **Second connection to the same catalog**: `otherProcess` (library_cache_test.go:18), `openService` (adopt_test.go:154), inline `sql.Open` in catalog_contention_test.go:30 and durability_test.go:232.
- **Source fixtures**: `writeSourceFile` (capture_test.go:33) vs `writeLines` (instructions_test.go:63) vs `probePut` (probe_test.go) vs inline `os.WriteFile` (~25×); Claude lines `claudeLine` vs `claudeInstructionLines` vs inline JSON; Codex `codexLines` vs `stableCodexSource` vs inline; **canonical export JSON written inline 6×** (archive_test ×3, durability_test, catalog_contention_test, mcp_conversations_test) vs `canonicalExport` (capture_index_guard_test.go:21); **Conductor DB schema written 6 different ways** (archive_test.go:909, adapter_titles_test.go:237, effort_test.go:284, capture_test.go:338/436, incremental_review_regression_test.go:186, `conductorFixture` capture_index_test.go:400).
- **HTTP call helpers** (NewRequest + `Bearer test-token` + recorder + decode): ~35 inline uses in 18 in-scope files plus named variants `probeCall`, `libraryStatusCall`, `delegationGET`, and out-of-scope `queryV2ReviewCall`, `queryExplorationCall`, `preferencesCall`, `serveTest`.
- **Poll-until loops** (`deadline := time.Now().Add(…)` + `time.Sleep(10ms)`): 15 in scope.
- **Pre-host schema rewind** written twice: `rewindToSchema4` (adopt_test.go:130) and inline in TestPreHostCatalogMigratesToLegacyHost (hosts_test.go:411).
- **Global seams set in random files**: `init(){commitFullFsync=false}` in durability_test.go:18; `useHost` in hosts_test.go; `parseHook`/`coverCheckHook` counters in capture_index_test.go.
- Helpers are scattered: 70 helper funcs are used from ≥2 other files (e.g., testCatalog 80 files, useHost 23, libraryFixture 15, seedToolCalls 13, ingestUsageFixture 13, writeSourceFile 11). In-scope tests depend on helpers living in out-of-scope **"review"/"v2" files** (`queryV2ReviewCall` in query_table_v2_review_test.go, `queryExplorationCall`, `metricRequest` in query_table_v2_test.go, `toolTextOf` in mcp_latency_test.go) — deleting those files would break home_query_recipes, machine_activity, effort and mcp_audit tests.

### Proposal: `internal/archive/harness_test.go` (same package; no import-cycle issues)
1. **TestMain** (moved from archive_test.go) sets every process-wide seam once: existing support-dir + `githubCLI` stub; `commitFullFsync=false` (moved from durability_test `init`); **`PHAROS_HOST_ID=test-host`** so the default `currentHost` never runs `ioreg`; **a `driveTool` stub** (canned plist / fail fast; it also backs the volume-identity lookup behind `storage()`) so `/api/health` and `/api/library/status` never shell out to diskutil/mdutil/hdiutil; tests that exercise the real tools opt in. Tests already stub it locally via `stubDriveTool`.
2. **Template catalog**. `testCatalog(t)`: on first use build one initialized catalog (fixed host), `Close()` (truncating checkpoint), keep its bytes; per test write the bytes to `t.TempDir()` and open through a path that **skips Initialize when `initializedByThisBuild()`** (that check already exists for `OpenCatalogForQuery`, catalog_query.go:101). Fix up `meta.legacy_host_id` to `currentHost().ID` after copy (Initialize records it at creation, hosts.go:178). Provide `freshCatalog(t)` (full Initialize) for the handful of tests whose subject is initialization/migration (TestNothingIsRecordedUnderAFallbackHostID, TestPreHostCatalogMigratesToLegacyHost, TestFTSRowidMigrationIndexesExistingRows, TestEffortMessageIndexCreatedAfterLegacyColumnMigration, TestAuthorshipUpgradeAddsWordColumns, adopt). Cheaper interim alternative: a `catalogSynchronous` var (`FULL` in prod, `OFF` in tests, restored by TestCatalogPoolAppliesDurabilityPragmas exactly as it already does for `commitFullFsync`) — measure both with `go test -run TestCatalogPoolEnforcesForeignKeys -count=20`.
3. **Process-level snapshot fixtures** (same copy-the-file mechanism): `libraryFixture`, finding month (fail every 2nd / every 4th day), `copiedFinding`, `legacyUpgradeCatalog`, `newLegacyInstall` (directory copy), `antigravityFixture`, `seedToolCalls(n)`.
4. **Seeding**: `execTx(t, db, stmts...)` (one transaction; replaces execSQL/run/exec closures), `ingestRecords(t, c, records...)` (one helper for the 13 inline tx copies), `memorySource(records...)` = today's `copyFixture` (hosts_test.go:147) for tests that only need records, avoiding JSONL write+parse.
5. **Source builders** in one place: `writeSourceFile`/`appendSourceFile`/`readText` (fold `writeLines` and most `probePut` uses), `claudeLine`, `codexLines`, `canonicalExport(t, file, workspaces...)`, `conductorDB(t, dir, sessions...)` with batched inserts and one schema (columns as a superset).
6. **Capture/index**: `captureTestConfig`, `runCapture`, `captureResult`, `readCaptureManifest`, `capturedPath`, `indexCaptures`, `ingestSource`, `countParses`, `countRows`, `assertCaptureRoundTrip`.
7. **Servers**: `testServer(t, catalog, config)` that registers `t.Cleanup(server.stop)` (today: 125 `NewServer` vs 8 `stop` calls); `apiCall(t, server, method, path, body) (int, map[string]any)` replacing the ~10 named/inline variants; `waitFor(t, timeout, cond)` replacing the 15 poll loops.
8. **Schema history**: `rewindToPreHostSchema(t, db)` shared by adopt and hosts tests.
9. **Tools out of the test binary**: TestAnalysisReplica, TestAuthorshipEvalLiveCatalog, TestDumpAuthorship, TestCaptureBenchmark, TestIncrementalScheduledResourceBudgets and the two benchmarks behind `//go:build tools` (or `cmd/pharos-dev`), so they stop compiling into every run.

---

## 4. Structural observations: splitting `internal/archive`

**Would it help?** Yes, materially, for both iteration and full runs — but it is a large refactor, and the
harness fixes in §0/§3 are larger and cheaper wins.
- *Iteration* (`go test -run X` averaging 32 s): all tests are white-box in one package, so **any** edit (even a
  `_test.go`) recompiles the 65k product lines plus 34k test lines as `archive [archive.test]`, runs `go vet`'s test
  subset over all of it, and relinks the binary with modernc SQLite. Splitting confines that to the edited package and
  its dependents. Zero-refactor tip for agent docs: `go test -vet=off -run X ./internal/archive` drops the vet pass.
- *Full runs*: no test uses `t.Parallel`, and many mutate package globals (`currentHost` via `useHost` in 23 files,
  `parseHook`, `captureFault`, `driveTool`, `githubCLI`, `commitFullFsync`, `syncAdapter`, `adoptFault`, `backupVolume`),
  so `t.Parallel` inside the package is unsafe without reworking those seams. Separate packages run as separate
  processes (`go test -p`) and cache results independently, which gives parallelism with the globals as they are.
  Today's wall time ≈ the serial sum (593 s, over the 10 min default timeout).

**Test clusters → candidate packages (this scope):**
- *source/parsers* (pure; ~16k product lines in files that never mention Catalog/Server/sql, e.g. adapters.go,
  adapter_tl1.go, adapter_antigravity.go, shell_command.go, tool_ledger.go, token_usage.go, findings_failures.go):
  archive_test parser tests, authorship classifier (authorship_test pure ~13, authorship_feedback 8), effort/harness/
  instructions parsing, launches `headlessProgram`, findings pure stats/wording/estimate, drive plist/checks, activity
  estimator. Fastest, highest-churn tests; biggest payoff.
- *capture / backup / adopt / drive* (capture.go, capture_sqlite.go, capture_plan.go don't touch Catalog; adopt does).
- *catalog core* (schema, ingest/copies, hosts, durability, contention, derived cache, incremental writer, capture indexing).
- *analysis* (tool rollup, findings, authorship ledger, delegation, launches) over the catalog DB.
- *query tables / library / usage / carbon* (+ home recipes, machine activity).
- *MCP* (mcp_*), *server/sync* (incremental_*, library_status, library_upgrade, activity, add-this-mac), *tl1*, *git/github*.

**Import-cycle and coupling risks seen:**
1. **80 product files define `*Catalog` methods** (28 define `*Server` methods). Go methods can't live outside the
   type's package, so every moved subsystem must become functions taking `*Catalog`/`*sql.DB` or an interface.
2. `Catalog` embeds subsystem state (`c.library`, `c.derived`, `c.findings`, `c.authorship`, `c.tools`, upgrade state)
   and `Initialize` calls `ensureAnalysisPartitions`, `ensureTL1Schema`, `ensureFindingsSchema` — core would import
   findings/tl1 while they import core. Needs schema-ensurer registration (core exposes a hook list) to break the cycle.
3. Ubiquitous helpers (`firstString` used 1,027×, `integer`, `jsonText`, `stableID`, `queryMaps`, `now`/`formatTime`)
   need a leaf util package first.
4. Seams/globals (`currentHost`, `parseHook`, `captureFault`, `commitFullFsync`) are read across subsystems; they must
   live in the lowest package that reads them, or be injected.
5. Tests are white-box (`catalog.library.mu`, `catalog.derived.entries`, `server.runs`, adapter internals). They must
   move with their code. Shared test helpers can't live in a package that imports the package under test (the in-package
   tests couldn't import it), so either a low-level `archivetest` with exported helpers per layer or duplication.
6. Adapters reach back into catalog state in a few places (e.g. antigravity `prepareInstalledVersions(c)`, Conductor
   resume `recordUsesCurrentExtractor`); those calls need an interface.
Suggested order: util leaf → `source` (parsers + records) → `capture` → keep the rest together until the harness work
lands.

---

## 5. Surprises

- **The full package exceeds the default 10-minute `go test` timeout** (go-test.json: FAIL 600.858 s). Any agent
  running plain `go test ./...` today gets a failure, not a result.
- **Real system access in ordinary tests:** the default `currentHost` runs `ioreg` (sync.OnceValue, hosts.go:41)
  for every test that doesn't call `useHost`; `/api/health` runs real `diskutil info -plist` via `storage()` →
  `cachedVolumeIdentity` → `lookupVolumeIdentity` → the unstubbed `driveTool` (drive.go:33/~410; the first poll waits up to
  `identityTimeout` = 2 s), and `driveHealth()` spawns a background diskutil/mdutil/hdiutil refresh
  (TestHealthReportsActiveSync, TestBackupAPIRunsInBackground). That refresh outlives the test; drive_test.go needs
  `quietDriveCache` (waits up to 30 s) to wait it out. TestHostIDSurvivesAnIoregFailure runs real `/usr/sbin/ioreg`.
  TestLibraryStatusNamesTheDriveAndIsIdleWhenNothingRuns asserts TMPDIR's volume is `internal`/non-ejectable, so it
  fails if TMPDIR sits on an external drive. Git subprocesses in 4 tests; `/bin/sh` gh stubs in 4. **No real network
  access** (TestMain points `githubCLI` at a nonexistent binary).
- **Leaked background work:** 125 `NewServer` calls vs 8 `server.stop`; `catalog.background = server.spawn` lets
  rebuilds and drive refreshes started in one test keep running into later ones. That is a plausible cause of
  unexplained outliers: TestMCPSearchMessagesPunctuation spent 9.2 s **before its first subtest** (inside
  `auditFixture` = one testCatalog + one insert); TestMCPConversationDiscoveryAndBoundedReading took 12.5 s for one
  ingest + 6 MCP calls.
- **Dead product code exposed by tests:** `activityDuration` (activity_timing.go:54) is read only by tests.
  `runFindingsPassReference` (findings.go:867) and `rebuildToolRollup` (tool_ledger_store.go:503) have no product
  callers — they are test oracles living in product code (fine, but worth knowing).
- **Skipped/compiled-only:** 4 env-gated tests + 2 benchmarks skip by default but still compile into every test binary;
  TestDumpAuthorship needs `-tags prototype`.
- **Weak assertions:** TestCaptureSnapshotsSQLiteConsistentlyWhileWritten only `t.Log`s when the writer never committed
  during capture; TestLibraryUpgradeWithoutGitHub's map values (`signedIn`) are both `false` and unused for "missing";
  TestEmbeddedUIIsServedWithoutPython checks a prefix and a brand string.
- **Timing-sensitive (possible flakes under load):** TestCheckpointGivesUpOnAnOldReader (`elapsed > 3s` fails),
  TestDriveHealthAnswersFromCacheAndRefreshesInBackground (`elapsed > identityTimeout+1s` = 1.2 s fails),
  TestBackupBoundsTheCatalogWAL (writer must outpace the snapshot), TestAdoptCopyIsTransactionallyConsistentUnderConcurrentCommits
  (busy-wait `runtime.Gosched` spin), TestIngestRetriesCatalogWriterContention (100 ms sleep vs 20 ms busy_timeout),
  TestDerivedValuesAreComputedOncePerCommit (10 ms sleep).
- **Global flipped by a test file's `init`:** durability_test.go sets `commitFullFsync=false` for the whole binary;
  tests that turn it on restore it to `false`, not to the previous value.
- **"Python" names are historical but the tests are valid:** TestStableIDsMatchPythonCatalog and
  TestSemanticVectorMatchesPythonImplementation pin IDs and embeddings that are persisted in existing catalogs. Rename,
  don't delete.
- **Parity tests are good oracles:** TestIncrementalToolRollupParity, TestIncrementalFindingFeatureParity and
  TestIncrementalWriterSearchParity compare incremental output to a full reference rebuild. That is the right design for
  derived tables in an archival product. Keep them and shrink their fixtures; don't delete them.
- **Data-safety tests deserve their cost:** adopt (snapshot of a running install, refusal paths, nested roots),
  backup (prune safety, credentials, WAL bound), capture (interruption resume, deleted-at-source retention, rewrite
  history), capture-index guards (older capture never rolls back newer data), hosts (cross-host prefix never shrinks,
  diverged union), durability (WAL truncation, pragmas). They are the only guards on data loss. Make them cheaper
  (template catalogs, once-built fixtures), and if anything gate the slowest behind `-short` for iteration. Don't delete them.

---

## Appendix: every in-scope test, with class and indicative seconds

| file:line | test | class | s (indicative) |
|---|---|---|---|
| activity_progress_test.go:13 | TestStageEstimateLearnsCurrentPaceAndHandlesPauses | B | 0.00 |
| activity_progress_test.go:47 | TestStageEstimateUsesWorkSizeAndRequiresComparableHistory | B | 0.00 |
| activity_progress_test.go:91 | TestStageEstimateDoesNotCountResumedWorkOrUnknownTotals | B | 0.00 |
| activity_progress_test.go:117 | TestMeasuredHistoryPersistsAndSeparatesWork | B | 0.38 |
| activity_progress_test.go:153 | TestActivityEstimatesResetWithStageSourceAndRun | B | 0.04 |
| activity_progress_test.go:186 | TestCaptureProgressUsesCurrentCopyBytes | B | 0.00 |
| activity_progress_test.go:204 | TestIndexProgressWeightsUnequalTranscripts | B | 0.00 |
| activity_progress_test.go:235 | TestLibraryUpdateReportsMeasuredCaptureAndIndexStages | B | 0.32 |
| activity_progress_test.go:285 | TestIndexProgressCountsOnlySuccessfulRecords | B | 0.28 |
| activity_progress_test.go:312 | TestIndexProgressMeasuresDatabaseSessionsByMessages | B | 0.36 |
| activity_timing_test.go:8 | TestActivityDurationHistory | S | 0.28 |
| activity_timing_test.go:50 | TestLibraryActivityCompletionEstimate | S | 0.24 |
| adapter_antigravity_test.go:167 | TestAntigravityTranscriptsBecomeConversations | B | 0.01 |
| adapter_antigravity_test.go:238 | TestAntigravityIngestIsIncrementalAndCaptured | I | 0.78 |
| adapter_antigravity_test.go:293 | TestProbeOffersAntigravityToMacsAlreadySetUp | B | 0.26 |
| adapter_antigravity_test.go:339 | TestAntigravityCapturePlanListsWhatTheAdapterReads | B | 0.01 |
| adapter_antigravity_test.go:370 | TestAntigravityPrintModeRunsAreHeadless | B | 0.79 |
| adapter_antigravity_test.go:430 | TestAntigravityPrintModeSurvivesSharedRunLogs | B | 0.01 |
| adapter_titles_test.go:10 | TestCodexSessionNamesSurviveCaptureAndTitleOnlyRename | B | 0.47 |
| adapter_titles_test.go:55 | TestCodexStateNamesPreferNewestSchemaAndDoNotUsePromptTitles | B | 0.63 |
| adapter_titles_test.go:138 | TestCodexNamingPathCannotOverrideDifferentSessionIdentity | B | 0.00 |
| adapter_titles_test.go:157 | TestClaudeCustomTitleAndIndexSummaryAreCaptured | B | 0.79 |
| adapter_titles_test.go:230 | TestConductorWorkspaceNameOnlyFillsPlaceholderSessionTitle | B | 0.01 |
| add_this_mac_test.go:11 | TestAddThisMacAddsCapturesAndIndexesInOneStep | B | 0.57 |
| adopt_test.go:271 | TestAdoptCopiesARunningInstallAsOneSnapshot | I | 0.75 |
| adopt_test.go:469 | TestAdoptKeepsARecordedLegacyHost | I | 0.68 |
| adopt_test.go:491 | TestAdoptIncludesTheWALOfAStoppedServiceAndLeavesItAlone | I | 1.05 |
| adopt_test.go:543 | TestAdoptCopyIsTransactionallyConsistentUnderConcurrentCommits | I | 0.96 |
| adopt_test.go:604 | TestAdoptFailuresLeaveNoLibrary | I | 1.77 |
| adopt_test.go:655 | TestAdoptRefusesUnsuitableInputs | I | 0.88 |
| adopt_test.go:703 | TestInitLibraryAdoptCommandLine | B | 0.00 |
| adopt_test.go:714 | TestAdoptRefusesLeftoverCatalogFiles | I | 0.77 |
| adopt_test.go:760 | TestAdoptFailsWhenTheCopyFailsItsCheck | I | 1.66 |
| adopt_test.go:797 | TestAdoptRefusesNestedArchiveRoots | I | 1.54 |
| adopt_test.go:878 | TestCopyPreservedRestoresHardLinksOnRetry | I | 0.02 |
| adopt_test.go:927 | TestAdoptRefusesAFallbackHostID | B | 0.60 |
| analysis_partitions_test.go:56 | TestIncrementalToolRollupParity | B | 2.83 |
| analysis_partitions_test.go:83 | TestIncrementalToolRollupBoundedAndNoop | P | 0.72 |
| analysis_partitions_test.go:116 | TestToolRollupConcurrentChangeSurvivesPublication | B | 0.53 |
| analysis_partitions_test.go:178 | TestIncrementalFindingFeatureParity | B | 1.02 |
| analysis_partitions_test.go:233 | TestCancelledAnalysesRemainDirty | B | 0.40 |
| analysis_partitions_test.go:250 | TestToolRollupMirrorsPricesAndRollover | B | 0.95 |
| analysis_partitions_test.go:280 | TestCompetingToolPublicationRejectsOldBuild | B | 0.50 |
| analysis_partitions_test.go:317 | TestFindingPromptInvalidatesWithoutTools | B | 0.72 |
| analysis_partitions_test.go:352 | TestFindingFirstUserCacheIndependentOfHelpSet | P | 0.84 |
| analysis_partitions_test.go:449 | TestFindingFeaturesOnlyRewriteChangedConversations | P | 0.66 |
| analysis_partitions_test.go:485 | TestFindingFeatureCorruptionRecovers | B | 0.45 |
| analysis_partitions_test.go:514 | TestFindingEvidenceTimestampTiesAreStable | B | 0.00 |
| analysis_partitions_test.go:522 | TestFindingFeaturesRetainConcurrentInputRevision | B | 0.40 |
| analysis_partitions_test.go:565 | TestFindingCallGroupsPreserveRawStrings | B | 0.48 |
| analysis_replica_test.go:16 | TestAnalysisReplica | P | skip |
| archive_test.go:62 | TestCatalogPoolEnforcesForeignKeys | D | 0.44 |
| archive_test.go:82 | TestReingestReplacesSessionMessageLinks | B | 0.38 |
| archive_test.go:113 | TestReingestFTSUsesRowidsAndSkipsUnchangedText | B | 0.42 |
| archive_test.go:171 | TestFTSRowidMigrationIndexesExistingRows | I | 0.41 |
| archive_test.go:192 | TestIngestSkipsUnchangedRecordsWhenSourceFingerprintChanges | B | 0.40 |
| archive_test.go:230 | TestLegacyConductorResumeChecksActivityTime | B | 0.39 |
| archive_test.go:262 | TestCodexJSONLReadsLargeEvents | I | 0.06 |
| archive_test.go:280 | TestCodexModelFollowsTurnContext | B | 0.41 |
| archive_test.go:318 | TestCodexCompactionTrigger | B | 0.01 |
| archive_test.go:356 | TestCodexMessagesToExistingAgentDoNotCreateSubagents | B | 0.00 |
| archive_test.go:382 | TestWorkflowToolIsDelegation | B | 0.00 |
| archive_test.go:402 | TestCodexParserUpgradeDoesNotSkipUnchangedFile | B | 0.33 |
| archive_test.go:429 | TestRepeatedNativeMessageIDDoesNotDuplicateSessionLink | B | 0.44 |
| archive_test.go:466 | TestSourceInventoryReportsConversationDocumentCoverage | B | 0.41 |
| archive_test.go:497 | TestStableIDsMatchPythonCatalog | B | 0.00 |
| archive_test.go:509 | TestSemanticVectorMatchesPythonImplementation | B | 0.00 |
| archive_test.go:523 | TestCodexRolloutKeepsFirstSessionIdentity | B | 0.00 |
| archive_test.go:556 | TestCodexForkWithParentSessionMetaUsesFilenameIdentity | B | 0.43 |
| archive_test.go:592 | TestCodexSubagentMetadataCreatesChildSession | B | 0.35 |
| archive_test.go:639 | TestClaudeSubagentHasOwnConversation | B | 0.35 |
| archive_test.go:688 | TestNativeToolEventsRetainIdentityResultsAndConversationFlow | B | 0.00 |
| archive_test.go:771 | TestCanonicalIngestAndAPIContract | B | 0.41 |
| archive_test.go:850 | TestHealthReportsActiveSync | B | 0.59 |
| archive_test.go:877 | TestAppCSPAllowsOnlyLocalBlobFormulaWorkers | B | 0.45 |
| archive_test.go:896 | TestConductorSemanticEventsRepositoryAndPR | B | 0.45 |
| archive_test.go:1034 | TestSourceTogglePreservesOtherConfig | B | 0.00 |
| archive_test.go:1055 | TestTL1RegistryIsReadOnlyAndIndexesNativeTask | B | 0.48 |
| archive_test.go:1115 | TestEmbeddedUIIsServedWithoutPython | D | 0.00 |
| archive_test.go:1125 | TestAppRoutesServeUIOnDirectNavigation | B | 0.42 |
| authorship_dump_test.go:16 | TestDumpAuthorship | D |  |
| authorship_eval_test.go:16 | TestAuthorshipEvalLiveCatalog | P | skip |
| authorship_feedback_test.go:19 | TestAuthorshipFeedbackFormats | B | 0.00 |
| authorship_feedback_test.go:58 | TestAuthorshipFeedbackDoesNotSwallowFollowingAgentOutput | B | 0.00 |
| authorship_feedback_test.go:80 | TestAuthorshipFeedbackCompactSequence | B | 0.00 |
| authorship_feedback_test.go:89 | TestAuthorshipFeedbackBoundaries | B | 0.00 |
| authorship_feedback_test.go:108 | TestAuthorshipFeedbackJSONEscapes | B | 0.00 |
| authorship_feedback_test.go:121 | TestAuthorshipFeedbackProtectsHumanFields | B | 0.00 |
| authorship_feedback_test.go:137 | TestAuthorshipFeedbackKeepsCopyEvidence | B | 0.00 |
| authorship_feedback_test.go:169 | TestAuthorshipFeedbackDoesNotClaimExamplesOrAutomatedNotes | B | 0.00 |
| authorship_feedback_test.go:191 | TestAuthorshipFeedbackLedger | B | 0.42 |
| authorship_test.go:29 | TestAuthorshipSeparatesHarnessAttachmentsAndSlashCommands | B | 0.00 |
| authorship_test.go:53 | TestAuthorshipAttributesSenders | B | 0.00 |
| authorship_test.go:71 | TestAuthorshipDetectsTemplatesButNotOneDayBroadcasts | B | 0.00 |
| authorship_test.go:92 | TestAuthorshipFindsCopiedTextWithinWindow | B | 0.00 |
| authorship_test.go:124 | TestAuthorshipIgnoresHarnessWhenMatchingResentText | B | 0.00 |
| authorship_test.go:135 | TestAuthorshipFlagsInterruptedOutputAndReusedLines | B | 0.00 |
| authorship_test.go:176 | TestAuthorshipFlagsPastedStructure | B | 0.00 |
| authorship_test.go:198 | TestAuthorshipFlagsIdentifiers | B | 0.00 |
| authorship_test.go:221 | TestAuthorshipTypingSpeedOnlyFlagsImpossibleRates | B | 0.00 |
| authorship_test.go:235 | TestAuthorshipRebuildCountsRepresentativeWorkOnce | B | 0.52 |
| authorship_test.go:308 | TestProseIndexServesAuthorshipQueries | C | 0.47 |
| authorship_test.go:332 | TestConversationQueriesAvoidProseIndex | P | 0.46 |
| authorship_test.go:354 | TestClaudeKeepsHarnessTextAndMarksHeadlessRuns | B | 0.00 |
| authorship_test.go:370 | TestCodexKeepsHarnessTextAndMarksExecRuns | B | 0.00 |
| authorship_test.go:383 | TestConductorSender | B | 0.00 |
| authorship_test.go:396 | TestAuthorshipUpgradeAddsWordColumns | I | 0.45 |
| authorship_test.go:420 | TestAuthorshipRebuildsAfterIndex | B | 0.40 |
| authorship_test.go:476 | TestAuthorshipManualRefreshHonorsRunCancellation | B | 0.66 |
| authorship_test.go:519 | TestAuthorshipInputsSeeMirrorLinksCachedBeforeARun | B | 0.37 |
| backup_test.go:128 | TestBackupCopiesTheLibraryIncrementally | I | 0.51 |
| backup_test.go:207 | TestBackupRefusesUnsafeDestinations | I | 0.41 |
| backup_test.go:262 | TestBackupWaitsForARunningCapture | I | 0.73 |
| backup_test.go:303 | TestBackupRefusesADestinationInUse | I | 0.27 |
| backup_test.go:323 | TestBackupCancelledRecordsNothing | B | 0.37 |
| backup_test.go:339 | TestBackupAPIRunsInBackground | B | 0.79 |
| backup_test.go:375 | TestBackupFollowsSymlinkedRootsAndNeverPrunesAnEmptyListing | I | 0.64 |
| backup_test.go:425 | TestBackupPruneKeepsFilesTheDestinationNamesDifferently | I | 0.82 |
| backup_test.go:457 | TestBackupIdentityTiesADestinationToOneLibrary | I | 1.72 |
| backup_test.go:488 | TestBackupKeepsTheLiveCatalogOutOfTrees | I | 1.20 |
| backup_test.go:534 | TestBackupRefusesDiskImagesOnTheSameDrive | B | 0.83 |
| backup_test.go:559 | TestBackupIsIncrementalOnCoarseTimestamps | I | 1.46 |
| backup_test.go:583 | TestBackupWithAMissingPartIsPartial | I | 1.61 |
| backup_test.go:613 | TestBackupRecordsAreNotLost | I | 1.59 |
| backup_test.go:632 | TestBackupLeavesOtherServicesCredentialsOut | B | 1.27 |
| backup_test.go:648 | TestBackupBoundsTheCatalogWAL | I | 2.47 |
| capture_bench_test.go:20 | TestCaptureBenchmark | P | skip |
| capture_index_guard_test.go:39 | TestOlderWholeSourceCaptureAfterAFailedOrInterruptedSync | I | 2.97 |
| capture_index_guard_test.go:76 | TestOlderTranscriptCaptureWithoutPartStateOrAfterAnExtractorChange | I | 2.67 |
| capture_index_guard_test.go:107 | TestOlderConductorCaptureLeavesNewerSessionsAlone | I | 2.70 |
| capture_index_guard_test.go:148 | TestResumedSyncStillIngestsChangedSessions | I | 0.90 |
| capture_index_guard_test.go:170 | TestOwnSyncEnrichesWhatAnotherMacIndexed | B | 0.96 |
| capture_index_guard_test.go:208 | TestFailedIndexLeavesTheHostsSourceStateAlone | I | 1.14 |
| capture_index_guard_test.go:230 | TestOnlyOneIndexOfACaptureRunsAtATime | I | 0.78 |
| capture_index_guard_test.go:254 | TestManifestPathsCannotLeaveTheCapture | I | 0.77 |
| capture_index_guard_test.go:292 | TestIndexMarkerSkipsSnapshotsReplacedWhileIndexing | B | 0.00 |
| capture_index_guard_test.go:308 | TestCaptureGuardWritesOnlyWhatIsNewer | I | 0.93 |
| capture_index_test.go:94 | TestIndexAttributesCapturesToTheCapturingHost | B | 0.56 |
| capture_index_test.go:172 | TestIndexAsksGitOnlyAboutThisMacsPaths | D | 1.06 |
| capture_index_test.go:207 | TestLiveIngestAndCaptureIndexShareWriterAndParts | I | 0.47 |
| capture_index_test.go:255 | TestIngestParsesOnlyChangedFiles | P | 0.49 |
| capture_index_test.go:303 | TestIngestRechecksPartsWhenIndexVersionChanges | B | 0.38 |
| capture_index_test.go:333 | TestCaptureStatusIgnoresNoOpCaptureAndReportsIndexerUpdate | B | 0.51 |
| capture_index_test.go:365 | TestClaudeSessionIsParsedWithItsSubagents | B | 0.32 |
| capture_index_test.go:420 | TestConductorParsesOnlyChangedSessions | B | 0.52 |
| capture_index_test.go:452 | TestIndexKeepsDeletedConductorSessionsFromUnindexedSnapshots | I | 0.95 |
| capture_index_test.go:506 | TestIndexResumesAfterInterruption | I | 0.49 |
| capture_index_test.go:541 | TestIndexWorkCountsWhatRemainsWithoutParsing | B | 0.37 |
| capture_index_test.go:602 | TestIndexPlanCountsAnUncountableSourceAsOneStep | B | 0.00 |
| capture_index_test.go:630 | TestIndexRunCountsConversationsBesideAnUncountableSource | B | 0.59 |
| capture_index_test.go:657 | TestIndexReadsOneVersionWhileACaptureReplacesIt | I | 0.59 |
| capture_index_test.go:689 | TestIndexTL1CaptureWithoutTheOriginalPaths | I | 1.03 |
| capture_index_test.go:748 | TestIndexTL1CaptureStoresAnalysisFromTheCapture | I | 0.46 |
| capture_index_test.go:811 | TestTL1EphemeralInstallationsResolveTemporaryPaths | B | 0.00 |
| capture_index_test.go:831 | TestIndexAPIRunsInBackground | B | 1.70 |
| capture_index_test.go:918 | TestIndexPassesOverWholeSourceCapturesOlderThanTheLastSync | D | 0.55 |
| capture_index_test.go:953 | TestStoppingAnIndexKeepsWhatItWrote | I | 0.49 |
| capture_index_test.go:1027 | TestStopAfterTheLastSourceChangesNothing | B | 0.27 |
| capture_index_test.go:1047 | TestStaleMetricLedgersAreIndexedAgain | B | 0.29 |
| capture_iopolicy_darwin_test.go:10 | TestCaptureIOPolicyIsScopedToTheCaptureThread | B | 0.00 |
| capture_sqlite_test.go:16 | TestSnapshotSQLiteStepsThroughOneReadTransaction | I | 0.62 |
| capture_test.go:104 | TestCaptureCopiesWhatAdaptersReadIncrementally | I | 0.36 |
| capture_test.go:171 | TestCaptureAppendsAndPreservesRewrittenFiles | I | 0.12 |
| capture_test.go:204 | TestCaptureKeepsFilesDeletedAtSource | I | 0.15 |
| capture_test.go:239 | TestCaptureResumesAfterInterruption | I | 0.37 |
| capture_test.go:331 | TestCaptureSnapshotsSQLiteConsistentlyWhileWritten | I | 3.34 |
| capture_test.go:429 | TestCaptureRetainsSnapshotGenerations | I | 0.31 |
| capture_test.go:487 | TestCaptureSeparatesHosts | B | 0.13 |
| capture_test.go:513 | TestCaptureTL1MapsRegistryPaths | I | 0.17 |
| capture_test.go:574 | TestCaptureRootDefaultsAndGuards | B | 0.00 |
| capture_test.go:626 | TestCaptureAPIRunsInBackgroundAndReportsSafeToUnplug | B | 0.58 |
| capture_test.go:680 | TestCaptureAPIReleasesItsLockWhenAStopBeginsFirst | B | 0.28 |
| capture_test.go:708 | TestCaptureExports | B | 0.06 |
| carbon_test.go:15 | TestCarbonFactorsFileIsValid | B | 0.00 |
| carbon_test.go:39 | TestCarbonValidationRejectsUncitedAndDisorderedFactors | B | 0.00 |
| carbon_test.go:54 | TestCarbonEnergyArithmetic | B | 0.00 |
| carbon_test.go:78 | TestCarbonHealthSplitsWindowsAndCategories | B | 0.47 |
| carbon_test.go:116 | TestCarbonSeriesFiltersAndHourlyTotals | B | 0.62 |
| carbon_test.go:204 | TestCarbonSeriesNoRepository | B | 0.84 |
| carbon_test.go:225 | TestCarbonSeriesNamesFallbacksAndUnclassifiedTokens | B | 0.99 |
| catalog_contention_test.go:13 | TestIngestRetriesCatalogWriterContention | B | 0.86 |
| catalog_contention_test.go:70 | TestCatalogWriterSummaryNamesLocalWriter | B | 0.61 |
| conversation_activity_test.go:10 | TestConversationActivityPreservesHumanGrainAndTime | B | 0.54 |
| conversation_search_test.go:10 | TestLibraryFindSearchesConversationsAndEvidence | B | 0.62 |
| conversation_search_test.go:69 | TestLibraryFindFuzzyBeyondTenThousandVocabularyTerms | B | 0.67 |
| delegation_cache_test.go:13 | TestDelegationCacheBoundsCompletedSnapshotsAndRefreshesLRU | B | 0.82 |
| delegation_cache_test.go:66 | TestDelegationCacheExpiresWithoutAnotherCommit | B | 0.98 |
| delegation_cache_test.go:104 | TestDelegationCacheDoesNotWarmAfterCommit | B | 0.42 |
| delegation_cache_test.go:147 | TestDelegationCacheKeepsInFlightComputationSharedDuringEviction | B | 0.60 |
| delegation_cache_test.go:216 | TestDelegationCacheOldCompletionKeepsNewerSnapshots | B | 0.49 |
| delegation_links_test.go:8 | TestDelegationCodexChildrenAcrossWorkspaces | B | 0.43 |
| delegation_links_test.go:95 | TestDelegationCodexLocalHandlesAndAmbiguity | B | 2.01 |
| delegation_links_test.go:135 | TestDelegationClaudeTaskReportErrorsPreserveLaunch | B | 2.59 |
| delegation_links_test.go:164 | TestDelegationLaunchErrorsStaySeparateFromReportErrors | B | 0.00 |
| delegation_test.go:50 | TestDelegationCanonicalHierarchyAndAccounting | B | 0.36 |
| delegation_test.go:95 | TestDelegationResultEvidenceKeepsConversationAndResultScope | B | 0.41 |
| delegation_test.go:128 | TestDelegationMessageCountsRetainMembershipAndUnknownTimes | B | 0.27 |
| delegation_test.go:158 | TestDelegationTopLevelConversationLabels | B | 2.33 |
| delegation_test.go:219 | TestDelegationAssignmentLabels | B | 8.69 |
| delegation_test.go:268 | TestDelegationEffortUsesOwnSessionAndConversation | B | 0.60 |
| delegation_test.go:283 | TestDelegationLegacyMessagesExcludeAlreadyOwnedEvents | B | 0.53 |
| delegation_test.go:309 | TestDelegationPaginationFilteringAndSnapshot | B | 0.57 |
| delegation_test.go:364 | TestDelegationTimelineWindowEvidenceAndErrorReferences | B | 0.53 |
| delegation_test.go:412 | TestDelegationCostFollowsUsageModelAndHistoricalDate | B | 0.31 |
| delegation_test.go:435 | TestDelegationDeepAncestorContextIsBounded | B | 0.78 |
| delegation_test.go:453 | TestDelegationLinksAcrossWorkspacesAndProviderBoundaries | B | 0.52 |
| delegation_test.go:482 | TestDelegationOwnershipCyclesDoNotBreakAnalysis | B | 0.46 |
| delegation_test.go:497 | TestDelegationFilesEditedDeduplicatesSuccessfulPaths | B | 0.61 |
| delegation_test.go:552 | TestDelegationDefaultPageSizeIsForty | C | 0.49 |
| derived_cache_test.go:11 | TestDerivedValuesAreComputedOncePerCommit | B | 0.66 |
| derived_cache_test.go:64 | TestCachesOutliveCommitsDuringARun | B | 0.55 |
| devui_test.go:13 | TestDevUIConcurrencyPrototypeIsOptionalAndAuthenticated | B | 0.00 |
| devui_test.go:57 | TestDevUIServesCheckoutAndForwardsAPIWithToken | B | 0.00 |
| devui_test.go:136 | TestDevUIDeepLoginPreservesFiltersAndReplacesStaleCookie | B | 0.00 |
| drive_test.go:94 | TestParsePlist | B | 0.00 |
| drive_test.go:110 | TestInspectVolumeReadsEncryptionSpotlightAndLocation | B | 0.00 |
| drive_test.go:157 | TestDriveChecksRecommendWithoutRunningAnything | B | 0.68 |
| drive_test.go:221 | TestDriveChecksForOtherSetups | B | 0.00 |
| drive_test.go:263 | TestDriveHealthAnswersFromCacheAndRefreshesInBackground | B | 0.80 |
| durability_test.go:42 | TestCatalogPoolAppliesDurabilityPragmas | I | 2.30 |
| durability_test.go:76 | TestSyncTruncatesWALWhileLibraryMonitorIsOpen | I | 0.67 |
| durability_test.go:114 | TestCloseEmptiesWALWhileAnotherProcessHasCatalogOpen | I | 0.42 |
| durability_test.go:140 | TestCheckpointGivesUpOnAnOldReaderWithoutHoldingUpWriters | I | 2.38 |
| durability_test.go:196 | TestKeepWALSmallTruncatesUnderContinuousReads | I | 0.82 |
| durability_test.go:230 | TestBeginWriteHoldsTheWriteLockFromItsFirstStatement | I | 0.36 |
| durability_test.go:259 | TestRequestReadsDoNotWaitForWriterConnections | B | 0.57 |
| effort_test.go:18 | TestEffortParsingPreservesTurnChangesAndUnknowns | B | 0.00 |
| effort_test.go:61 | TestEffortUsageAccountingQueriesAndExports | B | 0.81 |
| effort_test.go:203 | TestEffortUsageMigrationPreservesLegacyAccounting | I | 0.77 |
| effort_test.go:236 | TestEffortFromClaudeAndCanonicalSettings | B | 0.00 |
| effort_test.go:276 | TestEffortFromConductorColumnsAndEnvelopes | B | 0.54 |
| effort_test.go:332 | TestConductorSessionEffortUsesOnlyActiveAgentSetting | B | 0.00 |
| effort_test.go:353 | TestEffortLookupsUseCoveringMessageIndex | C | 0.63 |
| effort_test.go:376 | TestEffortMessageIndexCreatedAfterLegacyColumnMigration | I | 0.55 |
| effort_test.go:403 | TestEffortDoesNotAssignMixedAggregateToFinalSetting | B | 0.00 |
| effort_test.go:431 | TestEffortTL1InitialConfigurationRespectsTranscriptChanges | B | 0.00 |
| findings_detectors_test.go:67 | TestDriftDetector | B | 1.53 |
| findings_detectors_test.go:99 | TestCLIFrictionDetector | B | 1.00 |
| findings_detectors_test.go:130 | TestHeavyOutputAndOutlierDetectors | B | 13.35 |
| findings_detectors_test.go:170 | TestOrientationDetector | B | 0.99 |
| findings_detectors_test.go:196 | TestFindingsFollowRepositoryMerges | B | 1.34 |
| findings_estimate_test.go:9 | TestHeavyRemovable | B | 0.00 |
| findings_estimate_test.go:25 | TestFindingChangeKind | B | 0.00 |
| findings_estimate_test.go:40 | TestEstimateFinding | B | 0.00 |
| findings_estimate_test.go:65 | TestFindingTakesRecords | B | 0.00 |
| findings_estimate_test.go:78 | TestFindingPersistence | B | 0.00 |
| findings_review_test.go:12 | TestFindingSavingsSurviveTheWindow | B | 1.66 |
| findings_review_test.go:37 | TestFindingHandoffEverywhereWins | B | 0.00 |
| findings_review_test.go:48 | TestFindingCartRowsUnderEarlierIDs | B | 1.30 |
| findings_review_test.go:91 | TestFindingSnoozeUntilWorseStaysWoken | B | 1.17 |
| findings_review_test.go:131 | TestFindingDetectorPatterns | B | 0.00 |
| findings_review_test.go:166 | TestFindingObservationsMerge | B | 0.00 |
| findings_review_test.go:175 | TestFindingDismissAllAndDisableADetector | B | 0.81 |
| findings_test.go:12 | TestFindingStatistics | B | 0.00 |
| findings_test.go:35 | TestFindingWording | B | 0.00 |
| findings_test.go:53 | TestInstructionHunt | B | 0.00 |
| findings_test.go:78 | TestCommandRecovery | B | 0.00 |
| findings_test.go:100 | TestHeavyShape | B | 0.00 |
| findings_test.go:184 | TestFindingLifecycle | B | 1.27 |
| findings_test.go:336 | TestFindingDismissAndSnooze | B | 0.90 |
| findings_test.go:376 | TestFindingAliasesFollowRepositoryMerges | B | 0.00 |
| findings_test.go:388 | TestFindingPromptTargets | B | 0.00 |
| findings_verify_test.go:60 | TestFindingUnchangedMovesToTheNextChange | B | 1.33 |
| findings_verify_test.go:82 | TestFindingWorseAsksToUndo | B | 1.25 |
| findings_verify_test.go:106 | TestFindingInconclusiveWithoutWork | B | 0.93 |
| findings_verify_test.go:113 | TestFindingNotAppliedWithdraws | B | 0.94 |
| findings_verify_test.go:125 | TestFindingWinRegresses | B | 1.73 |
| findings_verify_test.go:150 | TestFindingCartMoveAndTick | B | 0.87 |
| findings_verify_test.go:184 | TestFindingCartCloneAndReorder | B | 1.01 |
| findings_verify_test.go:254 | TestFindingCheckpointsRecommend | B | 0.00 |
| git_integration_test.go:13 | TestMainIntegrationFindsMergeAndDirectHistory | I | 1.86 |
| git_integration_test.go:102 | TestMainIntegrationNegativeLookupCache | I | 2.28 |
| harness_version_test.go:12 | TestHarnessVersionsFromTranscripts | B | 0.00 |
| harness_version_test.go:36 | TestHarnessBackfillAndAlias | B | 1.00 |
| harness_version_test.go:65 | TestHarnessOptionalFieldsKeepDigest | B | 0.00 |
| harness_version_test.go:90 | TestInstalledVersionDoesNotChangeDigestOrPastConversation | B | 0.00 |
| harness_version_test.go:125 | TestInstalledVersionObservationMovesOnUpgrade | B | 0.83 |
| harness_version_test.go:161 | TestInstalledVersionUpsertPreservesFirstAndPastLast | B | 0.89 |
| harness_version_test.go:193 | TestHarnessBackfillRetriesCustomCaptureRoot | B | 1.06 |
| health_test.go:8 | TestHealthCountsStayCachedUntilACommit | B | 1.05 |
| health_test.go:49 | TestMessageCountFollowsEveryWrite | B | 1.18 |
| home_query_recipes_test.go:18 | TestHomeQueryRecipesV2 | B | 2.35 |
| home_query_recipes_test.go:91 | TestHomeQueryCardGroupedDestinations | B | 2.94 |
| hosts_test.go:26 | TestHostIDSurvivesAnIoregFailure | B | 0.15 |
| hosts_test.go:92 | TestNothingIsRecordedUnderAFallbackHostID | I | 1.38 |
| hosts_test.go:242 | TestHostsKeepSeparateSourceState | B | 1.24 |
| hosts_test.go:292 | TestIdenticalCopyFromAnotherHostIsOnlySighted | B | 1.45 |
| hosts_test.go:309 | TestRepairExistingIsHostScoped | B | 1.10 |
| hosts_test.go:330 | TestCrossHostPrefixNeverShrinksConversation | I | 1.37 |
| hosts_test.go:381 | TestDivergedCopiesAreUnionedUntilOneHoldsEverything | I | 0.73 |
| hosts_test.go:407 | TestPreHostCatalogMigratesToLegacyHost | I | 1.35 |
| incremental_bench_test.go:12 | TestIncrementalScheduledResourceBudgets | P | skip |
| incremental_bench_test.go:74 | BenchmarkIncrementalScheduled | P |  |
| incremental_recovery_regression_test.go:15 | TestToolRollupPublicationRetriesWithoutRebuildingTemporaryCube | B | 1.08 |
| incremental_recovery_regression_test.go:76 | TestReferenceAuditIgnoresMaterializedDeferredToolCube | B | 2.60 |
| incremental_recovery_regression_test.go:105 | TestSyncStatusExposesLowercaseSourceChoices | B | 0.69 |
| incremental_recovery_regression_test.go:129 | TestNativeAliasLookupUsesIndexedAccountAndNativeID | C | 0.74 |
| incremental_recovery_regression_test.go:140 | TestDeferredIdentityReconciliationHonorsCancellation | B | 0.67 |
| incremental_recovery_regression_test.go:152 | TestVerificationIncludesDeletedConductorSessionsFromOlderCaptures | I | 3.20 |
| incremental_recovery_regression_test.go:182 | TestRetainedRepairBypassesLedgerFingerprintsAndTokenMetrics | I | 2.38 |
| incremental_recovery_regression_test.go:224 | TestFullRecoveryCapturesSourceNotPreviouslyCaptured | I | 2.48 |
| incremental_recovery_regression_test.go:262 | TestAntigravityUnreadableRequestWALDoesNotAdvanceDiscovery | D | 0.01 |
| incremental_review_regression_test.go:18 | TestFullRecapturePreservesOnlyChangedFileVersions | I | 0.74 |
| incremental_review_regression_test.go:51 | TestAntigravityMovingGroupDoesNotAbortHealthySibling | B | 0.03 |
| incremental_review_regression_test.go:117 | TestAntigravityDependencyFailureDoesNotAbortHealthySibling | B | 0.00 |
| incremental_review_regression_test.go:148 | TestJSONLDiscoveryToleratesOnlyDisappearingEntries | B | 0.00 |
| incremental_review_regression_test.go:177 | TestConductorObservedVersionFollowsBlockedSnapshotRead | I | 0.38 |
| incremental_review_regression_test.go:234 | TestVerifyAllEndpointChecksEveryInputIncludingLargeConductorSession | I | 13.32 |
| incremental_review_regression_test.go:289 | TestUnlimitedManualVerificationBypassesAutomaticSizeGate | B | 2.01 |
| incremental_review_regression_test.go:304 | TestSyncSettingsRouteServesUI | D | 1.72 |
| incremental_review_regression_test.go:315 | TestAntigravityAuditExcludesUnrelatedRunLogEvidence | B | 2.49 |
| incremental_review_regression_test.go:330 | TestAntigravityLogReaderHandlesChunkBoundariesAndLongLines | B | 0.03 |
| incremental_review_regression_test.go:349 | TestIncrementalActivityEstimatesKeepRunKindsSeparate | S | 0.59 |
| incremental_review_regression_test.go:381 | TestIndependentAuditPreservesCrossRepositoryToolPathContext | B | 1.68 |
| incremental_sync_test.go:17 | TestScheduledSyncDefersAnalysesAndManualSyncRefreshesGitHub | B | 2.61 |
| incremental_sync_test.go:72 | TestSyncCheckStartsManualRun | B | 1.53 |
| incremental_sync_test.go:97 | TestManualSyncFailedAnalysisRetainsDirtyGeneration | B | 6.50 |
| incremental_sync_test.go:111 | TestManualSyncRefreshesRecentHumanWords | B | 1.84 |
| incremental_sync_test.go:218 | TestChangesAPIReadsNoBodiesOrState | B | 0.58 |
| incremental_sync_test.go:238 | TestAutomaticRefreshKeepsIntentionalStateAndAuditsNoops | B | 3.79 |
| incremental_sync_test.go:308 | TestSyncHistoryRestoresLegacyCompletionTime | B | 1.24 |
| incremental_sync_test.go:333 | TestIndependentAuditDetectsCorruptionAndResolvesOnlyAfterRepair | I | 7.29 |
| incremental_sync_test.go:376 | TestAuditDetectsMissingTrigramAndUnindexedStableUnit | I | 3.00 |
| incremental_sync_test.go:407 | TestManualActionPreemptsAutomaticWithoutConflict | B | 2.77 |
| incremental_sync_test.go:438 | TestSyncCadenceValidationAndPersistence | B | 1.63 |
| incremental_sync_test.go:465 | TestFullRecaptureBypassesReuseAndResumesVerifiedFiles | D | 0.20 |
| incremental_sync_test.go:489 | TestConductorAutomaticWALAndSameSignalAudit | I | 1.82 |
| incremental_sync_test.go:519 | TestAuditBudgetsAndSourceMovementDoNotCreateIssues | B | 0.64 |
| incremental_sync_test.go:537 | TestVerificationVisitsEveryStableInputAndWorkspaceMetrics | I | 1.85 |
| incremental_sync_test.go:612 | TestAutomaticAuditRetriesOpenMismatchFoundByOtherCode | B | 2.85 |
| incremental_sync_test.go:629 | TestAutomaticAuditLeavesUnchangedMismatchToTheRotation | B | 3.47 |
| incremental_sync_test.go:645 | TestRecoveryAuditsEveryOpenIssueAgain | B | 3.45 |
| incremental_sync_test.go:658 | TestVerificationReadsOffHostCapturesAfterSourceRemoval | I | 1.52 |
| incremental_sync_test.go:687 | TestAutomaticProjectionsAreBatchedAndRollupRemainsDeferred | B | 11.49 |
| incremental_sync_test.go:710 | TestAntigravityMembershipRemovalReparentingAndRootPromotion | B | 1.05 |
| incremental_sync_test.go:748 | TestConductorNativeAliasesLinkInEitherArrivalOrder | B | 1.48 |
| incremental_writer_test.go:54 | TestIncrementalWriterSearchParity | B | 12.00 |
| incremental_writer_test.go:101 | TestUpsertMessageCompleteNoop | P | 1.34 |
| incremental_writer_test.go:123 | BenchmarkIncrementalWriter | P |  |
| incremental_writer_test.go:168 | TestIncrementalWriterRowBudget | P | 3.32 |
| instructions_test.go:87 | TestClaudeInstructionsFromTranscript | B | 0.00 |
| instructions_test.go:124 | TestCodexInstructionsFromRollout | B | 0.00 |
| instructions_test.go:156 | TestInstructionsIngestRoundTrip | B | 3.63 |
| instructions_test.go:223 | TestInstructionsBackfill | B | 0.42 |
| launches_test.go:10 | TestHeadlessProgram | B | 0.00 |
| launches_test.go:33 | TestAuthorshipLinksHeadlessRunsToLaunchingCommands | B | 1.16 |
| library_cache_test.go:32 | TestLibraryCacheSeesCommitsFromAnotherConnection | B | 0.82 |
| library_cache_test.go:77 | TestLibraryCacheRepricesOnPriceBookAndDateChanges | B | 0.75 |
| library_cache_test.go:122 | TestLibraryCacheServesConcurrentRequestsWithoutSharingPages | B | 2.54 |
| library_cache_test.go:170 | TestWarmCachesRebuildsWhatPagesUse | B | 1.59 |
| library_find_filter_test.go:13 | TestLibraryQueryTableCombinesKeywordSearchWithFilters | B | 1.60 |
| library_find_filter_test.go:78 | TestLibraryFindFiltersBeforeGlobalCandidateLimit | B | 6.07 |
| library_status_test.go:39 | TestLibraryStatusNamesTheDriveAndIsIdleWhenNothingRuns | B | 0.71 |
| library_status_test.go:64 | TestUnplugAdviceAlwaysSaysEject | B | 0.00 |
| library_status_test.go:90 | TestLibraryStatusListsEverythingHoldingTheLibrary | B | 0.69 |
| library_status_test.go:233 | TestLibraryRefreshCanBeStoppedAndResumed | B | 0.73 |
| library_status_test.go:290 | TestGitRefreshIsTrackedWhileItRuns | B | 0.82 |
| library_status_test.go:312 | TestBackupStatusSuggestsADestination | B | 0.56 |
| library_status_test.go:330 | TestLibraryStatusNamesTheVolumeFromTheDriveChecks | B | 0.61 |
| library_test.go:80 | TestLibraryStoredRowsMatchLiveComputation | B | 0.40 |
| library_test.go:112 | TestLibraryReflectsEveryDependencyBeforeAndAfterRefresh | B | 0.52 |
| library_test.go:169 | TestLibraryQueryTableServesArbitrarySortsAndFilters | B | 0.52 |
| library_test.go:251 | TestLibraryProjectionRebuildsWhenDefinitionChanges | B | 0.77 |
| library_test.go:278 | TestReopenedCatalogHasNoLegacyQueryStats | D | 0.67 |
| library_test.go:292 | TestLibraryRequestsReadingFewerFieldsMatchFullRows | B | 1.68 |
| library_test.go:389 | TestLibraryDefaultPageReadsNoLongTextForOtherRows | P | 2.71 |
| library_test.go:450 | TestLibraryPreviewsFollowTheirMessages | B | 0.82 |
| library_test.go:506 | TestLibraryRowsReadTheWorkspaceIndex | P | 1.07 |
| library_test.go:519 | TestLibraryRefreshReadsMetricsThroughTheirWorkspace | P | 1.16 |
| library_upgrade_test.go:64 | TestUpgradeStatusReportsWholeJobProgress | B | 0.98 |
| library_upgrade_test.go:82 | TestIndexCanContinueWithPendingUpgrade | B | 1.23 |
| library_upgrade_test.go:107 | TestIndexSkipsUpgradeWhenLibraryIsCurrent | B | 2.69 |
| library_upgrade_test.go:183 | TestLibraryUpgrade | I | 1.10 |
| library_upgrade_test.go:259 | TestLibraryUpgradeWhileLibraryRefreshes | I | 1.09 |
| library_upgrade_test.go:299 | TestLibraryUpgradeAsksGitHubWithoutAFlag | B | 0.85 |
| library_upgrade_test.go:347 | TestLibraryUpgradeWithoutGitHub | B | 1.80 |
| library_upgrade_test.go:375 | TestLibraryUpgradeRepairsSplitRepositories | I | 3.69 |
| library_upgrade_test.go:473 | TestRefreshRepositoryForgeIDsMergesNewMoves | B | 1.22 |
| machine_activity_test.go:15 | TestMachineTimeQueries | B | 0.87 |
| machine_activity_test.go:79 | TestMachineOperationFamilies | B | 0.00 |
| machine_activity_test.go:98 | TestMachineTimeCompleteAggregationBeyondPreviewLimit | B | 2.25 |
| mcp_audit_test.go:53 | TestMCPSearchMessagesPhrasesAndFilters | B | 1.33 |
| mcp_audit_test.go:109 | TestMCPSearchMessagesPunctuation | B | 9.51 |
| mcp_audit_test.go:132 | TestMCPSearchMessagesUnicode | B | 0.39 |
| mcp_audit_test.go:150 | TestMCPSearchMessagesExhaustivePagination | B | 8.34 |
| mcp_audit_test.go:194 | TestMCPAnchoredMessageWindowContinuation | B | 0.53 |
| mcp_audit_test.go:262 | TestMCPReadableMessageWindows | B | 0.31 |
| mcp_audit_test.go:336 | TestMCPArchiveStatus | B | 0.53 |
| mcp_audit_test.go:403 | TestMCPArchiveStatusEmptyAndCurrent | B | 0.57 |
| mcp_audit_test.go:419 | TestMCPArchiveStatusDoesNotCallPartialIndexSuccessful | B | 0.35 |
| mcp_audit_test.go:439 | TestMCPArchiveStatusSuccessfulNoOpSyncRefreshesLag | B | 4.38 |
| mcp_audit_test.go:502 | TestMCPAuditFreshnessAgreesAcrossTools | B | 5.11 |
| mcp_audit_test.go:548 | TestMCPAuditToolsExposedAndLogged | B | 0.81 |
| mcp_conversations_test.go:11 | TestMCPConversationDiscoveryAndBoundedReading | B | 12.50 |
| mcp_history_test.go:13 | TestMCPRequestedArchiveSourceProjection | B | 0.68 |
| mcp_history_test.go:59 | TestMCPControlAndCallHistory | B | 0.63 |
