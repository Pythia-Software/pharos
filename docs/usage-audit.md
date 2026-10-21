# Usage audit

Status: **Complete**. Updated 2026-10-20T23:19:42.833Z. 432 completed cases across 17 pages. Selected runs: `audit-01`, `audit-corrections`, `audit-controls`, `audit-time-coverage`.

Primary views: **235 Green, 179 Yellow, 15 Red, 3 Black**. 16 cases returned an error or failed to settle. Fast error responses still count as errors.

## Method

The audit drives the real Chrome UI and service against the existing isolated development library. It runs serially, intercepts preference writes inside each browser context, and leaves the source library and saved settings unchanged. Each case varies valid dates, categories, keywords, numeric/null/OR filters, grouping, formulas, sorting, or dashboard controls. Empty results require a completed response. Fresh-context URL replays use different saved display preferences to detect hidden state.

Timings include data requests and rendering. A case uses the slowest of initial navigation, changed-control completion, and URL replay. **Green <1 s; Yellow 1–<4 s; Red 4–<30 s; Black ≥30 s without a settled result.** Errors are counted separately from latency. Linked queries have their own timing summary below. These are local measurements with warm service caches, not guarantees for other hardware or cold catalogs.

For views that settle, every distinct dashboard query/record link is executed. Up to five representative outgoing links per dashboard also open in fresh browsers. Equivalent queries and records/search links reuse a native execution within the frozen snapshot, including its original failure if it timed out. Native grouped results inspect a source group when one exists. Query pages check their dashboard return link. This samples the large combination space; it does not prove all possible queries work.

The complete canonical query is compared with the resulting URL: source predicates, columns, sorting, limits, offsets, metrics, grouping, and HAVING. Singleton-OR normalization is equivalent. Default, reduced-column, and paged Tool summary and Skill evidence cases also check row actions. Additional targeted cases cover chart-only legend choices, expanded series, and Parallelism selection/zoom. Passive distinct-value suggestions do not gate readiness.

429/432 cases completed fresh-context replay. A timed-out primary view can block replay and outgoing checks; those unexercised checks are not passes.

Error counts include failed filter-choice lookups (`/distinct`) even if the requested rows rendered. Those auxiliary failures are listed separately in the inventory. Successfully handled metric-budget fallbacks are recoveries rather than visible view failures.

69 requested control interactions completed. 28 optional interactions were unavailable in that filtered view; those cases still tested initial loading and URL replay.

## View results

| Page | Cases | Green | Yellow | Red | Black | Errors | URL issues | Link issues | p50 | p95 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Tokens & cost dashboard | 27 | 19 | 8 | 0 | 0 | 0 | 2 | 22 | 0.91 s | 1.21 s |
| Human words dashboard | 27 | 13 | 14 | 0 | 0 | 0 | 3 | 22 | 1.02 s | 1.69 s |
| Agent time dashboard | 25 | 12 | 6 | 4 | 3 | 3 | 0 | 21 | 1.50 s | 30.00 s |
| Parallelism dashboard | 27 | 11 | 16 | 0 | 0 | 0 | 0 | 0 | 1.05 s | 1.50 s |
| Tools dashboard | 25 | 5 | 17 | 3 | 0 | 0 | 0 | 22 | 1.89 s | 4.73 s |
| Pharos MCP dashboard | 25 | 24 | 1 | 0 | 0 | 0 | 0 | 0 | 0.62 s | 0.96 s |
| Skills dashboard | 26 | 25 | 1 | 0 | 0 | 0 | 1 | 22 | 0.60 s | 0.95 s |
| Carbon impact dashboard | 25 | 20 | 5 | 0 | 0 | 0 | 25 | 22 | 0.85 s | 1.20 s |
| Token usage query | 25 | 1 | 24 | 0 | 0 | 0 | 0 | 22 | 2.36 s | 3.06 s |
| Conversation activity query | 25 | 2 | 23 | 0 | 0 | 0 | 0 | 22 | 2.54 s | 3.04 s |
| Human words by conversation query | 25 | 20 | 5 | 0 | 0 | 0 | 0 | 22 | 0.77 s | 1.28 s |
| Human messages query | 25 | 10 | 15 | 0 | 0 | 6 | 0 | 22 | 1.34 s | 2.98 s |
| Tool summary query | 25 | 15 | 10 | 0 | 0 | 0 | 0 | 24 | 0.80 s | 1.60 s |
| Tool calls query | 25 | 4 | 13 | 8 | 0 | 7 | 0 | 22 | 3.49 s | 12.83 s |
| MCP calls query | 25 | 25 | 0 | 0 | 0 | 0 | 0 | 22 | 0.55 s | 0.72 s |
| Skill evidence query | 25 | 23 | 2 | 0 | 0 | 0 | 0 | 24 | 0.64 s | 1.13 s |
| Parallel activity query | 25 | 6 | 19 | 0 | 0 | 0 | 0 | 22 | 1.60 s | 2.38 s |

## Linked-query timing

Measured 328 outgoing executions: Green 221; Yellow 79; Red 24; Black 4. p50 0.39 s, p95 4.93 s. Browser transitions include a fresh context; API transitions include native query preparation and execution.

## Findings and next actions

1. **Broad raw-call execution needs attention.** Agent time and Tool calls include slow or timed-out primary views and drilldowns. Profile their scan/materialization paths and validate bounded variants.
2. **Keyword queries can fail at the exact-query budget.** Human messages and Tool calls contains/excludes combinations can return the 2.5-second guardrail error. Narrowing advice is visible, but the requested query has not succeeded.
3. **Chart-only state is missing from URLs.** Carbon assumptions and legend/expanded-series choices can change after fresh-context replay. Serialize those choices with the dashboard state.
4. **Transitions need explicit filter semantics.** Full current analysis / More tool analyses reset dashboard filters. Back to dashboard drops query edits. Search conversation text also opens an unfiltered Library. Preserve supported filters and label intentional resets.
5. **Row actions need complete source identity.** Tool summary Calls can open an empty population; Skill evidence can lose the source workspace/conversation. Fetch the complete row by ID when the projection omits those fields.

Relevant implementation: [dashboard/query transitions](../web/src/usage-workspace.tsx), [row actions and chart state](../web/src/index.tsx), [Carbon assumptions](../internal/archive/assets/carbon.js), [query budgets](../internal/archive/query_table_v2.go). These are follow-up findings; the audit did not alter the UI or query engine.

## Failure inventory

Handled metric-budget recoveries occurred in 2 cases. Their full fallback duration remains in the timing bands; the initial diagnostic remains in private evidence.

| Kind | Finding | Cases | Examples |
| --- | --- | ---: | --- |
| Link | Full current analysis: filters reset without an explicit reset label | 107 | `dashboard-tokens-02`, `dashboard-tokens-03`, `dashboard-tokens-04`, `dashboard-tokens-05` |
| URL | selected differ after fresh-context URL replay | 28 | `dashboard-human-13`, `dashboard-carbon-01`, `dashboard-carbon-02`, `dashboard-carbon-03` |
| URL | legends differ after fresh-context URL replay | 6 | `dashboard-human-13`, `dashboard-tokens-26`, `dashboard-tokens-27`, `dashboard-human-26` |
| Link | Open grouped query: The operation was aborted due to timeout | 5 | `dashboard-time-01`, `dashboard-time-02`, `dashboard-time-04`, `dashboard-time-08` |
| Error | No settled result after 30 seconds; pending=/api/query/tool_calls/metrics; responses=1; busy=1 | 1 | `dashboard-time-03` |
| Link | More tool analyses: filters reset without an explicit reset label | 22 | `dashboard-tools-02`, `dashboard-tools-03`, `dashboard-tools-04`, `dashboard-tools-05` |
| URL | controls differ after fresh-context URL replay | 25 | `dashboard-carbon-01`, `dashboard-carbon-02`, `dashboard-carbon-03`, `dashboard-carbon-04` |
| Link | Back to dashboard: query source filters reset without an explicit reset label | 198 | `query-usage-02`, `query-usage-03`, `query-usage-05`, `query-usage-06` |
| Link | Search conversation text: record filters reset without an explicit reset label | 9 | `query-usage-25`, `query-conversation_activity-25`, `query-writing-25`, `query-writing_messages-25` |
| Error | /api/query/writing_messages/rows-v2: HTTP 400 This exact record query exceeded 2.5 seconds. Add a workspace, conversation, or date filter, or use Library's bounded text search for broad searches. | 6 | `query-writing_messages-07`, `query-writing_messages-08`, `query-writing_messages-10`, `query-writing_messages-15` |
| Error | This exact record query exceeded 2.5 seconds. Add a workspace, conversation, or date filter, or use Library's bounded text search for broad searches. | 11 | `query-writing_messages-07`, `query-writing_messages-08`, `query-writing_messages-10`, `query-writing_messages-15` |
| Error | Replay: This exact record query exceeded 2.5 seconds. Add a workspace, conversation, or date filter, or use Library's bounded text search for broad searches. | 11 | `query-writing_messages-07`, `query-writing_messages-08`, `query-writing_messages-10`, `query-writing_messages-15` |
| Link | Summary Calls action opens an empty population for a nonempty summary | 3 | `query-tools-01`, `query-tools-04`, `query-tools-24` |
| Link | Skill Evidence action loses the source identity when its columns are hidden | 3 | `query-skill_usages-01`, `query-skill_usages-04`, `query-skill_usages-24` |
| Error | No settled result after 30 seconds; pending=/api/query/tool_calls/query-v3; responses=2; busy=1 | 2 | `dashboard-time-06`, `dashboard-time-18` |
| Link | Open grouped query: /api/query/tool_calls/distinct: HTTP 408 context deadline exceeded | 1 | `dashboard-time-12` |
| Error | /api/query/tool_calls/rows-v2: HTTP 400 This exact record query exceeded 2.5 seconds. Add a workspace, conversation, or date filter, or use Library's bounded text search for broad searches. | 5 | `query-tool_calls-07`, `query-tool_calls-08`, `query-tool_calls-10`, `query-tool_calls-15` |
| Error | /api/query/tool_calls/distinct: HTTP 408 context deadline exceeded | 2 | `query-tool_calls-20`, `query-tool_calls-23` |

## Slow views

| Case | Band | Time | API maximum |
| --- | --- | ---: | ---: |
| dashboard-time-02 | Red | 11.79 s | 11.28 s |
| dashboard-time-03 | Black | 30.00 s | Incomplete |
| dashboard-tools-13 | Red | 7.52 s | 4.97 s |
| dashboard-tools-14 | Red | 4.73 s | 1.91 s |
| dashboard-tools-18 | Red | 4.06 s | 2.22 s |
| dashboard-time-05 | Red | 4.83 s | 4.53 s |
| dashboard-time-06 | Black | 30.00 s | Incomplete |
| dashboard-time-12 | Red | 5.02 s | 4.69 s |
| dashboard-time-14 | Red | 4.88 s | 4.66 s |
| dashboard-time-18 | Black | 30.00 s | Incomplete |
| query-tool_calls-03 | Red | 5.76 s | 11.01 s |
| query-tool_calls-05 | Red | 4.61 s | 3.90 s |
| query-tool_calls-06 | Red | 4.29 s | 3.82 s |
| query-tool_calls-09 | Red | 4.82 s | 4.18 s |
| query-tool_calls-11 | Red | 4.62 s | 4.12 s |
| query-tool_calls-12 | Red | 7.56 s | 6.68 s |
| query-tool_calls-20 | Red | 12.83 s | 11.66 s |
| query-tool_calls-23 | Red | 23.81 s | 23.07 s |

## Slow outgoing queries

| Case | Transition | Mode | Time |
| --- | --- | --- | ---: |
| dashboard-time-04 | Open grouped query | failed | 30.06 s |
| dashboard-time-01 | Open grouped query | failed | 30.01 s |
| dashboard-time-08 | Open grouped query | failed | 30.00 s |
| dashboard-time-25 | Open grouped query | failed | 30.00 s |
| dashboard-time-09 | Open grouped query | api | 28.64 s |
| dashboard-time-02 | Search / filter records | browser | 25.67 s |
| dashboard-time-20 | Open grouped query | api | 23.65 s |
| dashboard-time-21 | Open grouped query | api | 18.32 s |
| dashboard-time-12 | Open grouped query | failed | 15.47 s |
| dashboard-time-24 | Open grouped query | browser | 14.46 s |
| dashboard-time-10 | Open grouped query | api | 13.35 s |
| dashboard-time-14 | Open grouped query | api | 9.81 s |
| dashboard-time-05 | Open grouped query | api | 7.49 s |
| dashboard-time-07 | Open grouped query | api | 6.59 s |
| dashboard-time-15 | Open grouped query | api | 6.44 s |
| dashboard-tools-18 | Open grouped query | api | 6.21 s |
| dashboard-tokens-08 | Search / filter records | browser | 4.93 s |
| dashboard-tokens-15 | Open grouped query | browser | 4.84 s |
| dashboard-time-17 | Open grouped query | api | 4.82 s |
| dashboard-tokens-01 | Open records | browser | 4.80 s |
| dashboard-tokens-18 | Open grouped query | browser | 4.77 s |
| dashboard-time-22 | Open grouped query | api | 4.67 s |
| dashboard-tools-24 | Open grouped query | browser | 4.43 s |
| dashboard-tools-25 | Open grouped query | api | 4.42 s |
| dashboard-tokens-20 | Search / filter records | browser | 4.41 s |
| dashboard-tokens-16 | Open records | browser | 4.13 s |
| dashboard-tools-03 | Open grouped query | browser | 4.09 s |
| dashboard-tokens-25 | Open grouped query | api | 4.04 s |

## Reproduction and evidence

Run `node tools/audit-usage.mjs --run NAME` against the existing dev copy. Use `--pages dashboard:tokens,query:usage` to target selected pages, `--cases 25` for the matrix, `--ids dashboard-carbon-06` for a specific reproduction, and the same run name to resume checkpoints. `--controls --cases 30` includes targeted control cases. `--origin` and `--config` select another isolated service. Only one audit runs at a time. `tools/summarize-usage-audit.mjs --runs BASELINE,CORRECTIONS,CONTROLS` combines explicitly selected evidence.

Use `--in-place` on a selected dashboard run to exercise live in-app transitions. `node tools/audit-usage-link-transport.mjs --runs RUNS` re-executes saved ordinary drilldowns using the records endpoint. To reproduce this report, pass all four main runs to the summary tool, plus `--links .context/usage-audit/link-transport/results.jsonl --navigation .context/usage-audit/audit-navigation/results.jsonl`. `--summary FILE` emits the machine-readable inventory and `--validation FILE` includes check results.

Private manifests contain the exact case URLs and runtime category samples; JSONL checkpoints contain request/transition timings and failure evidence. They live under `.context/usage-audit/<run>/`. Query values and credentials are excluded from this report.

Ordinary record-link probes were reconciled through `/rows-v2`, matching the UI, including its exact-search budget. Initial flat-link V3 probes are excluded from the reported navigation failures and linked timing. Grouped links retain native V3 execution. The corrected flat-link sweep ran after the baseline with warm caches, so its latency is reported separately from initial page measurements.

Two early Agent time checkpoints predated saved link targets. Supplemental reads recovered those targets only after verifying identical view URLs, retaining the original primary-page timings. Pass `--link-sources .context/usage-audit/audit-early-links/results.jsonl` to both link-transport and summary tools to reproduce that reconciliation.

The selected corrections exclude read-only Carbon labels from interactive legend checks and compare normalized predicates. A generator test caught repeated Agent time combinations; the affected cases were replaced with distinct time-bucket combinations. Completion requires at least 25 distinct initial parameter URLs on each page. Targeted control cases can share a starting URL while exercising different actions.

- audit-01: frontend SHA-256 `3eaa460e80825e2009728b5690add069f706c98b4de7349d13f720515cb9737c`.
- audit-corrections: frontend SHA-256 `3eaa460e80825e2009728b5690add069f706c98b4de7349d13f720515cb9737c`.
- audit-controls: frontend SHA-256 `3eaa460e80825e2009728b5690add069f706c98b4de7349d13f720515cb9737c`.
- audit-time-coverage: frontend SHA-256 `3eaa460e80825e2009728b5690add069f706c98b4de7349d13f720515cb9737c`.

## Records transport correction

106 distinct ordinary record queries were re-executed through the UI records endpoint: 106 succeeded and 0 failed. Each result applies to every dashboard link with the same dataset and canonical query. Earlier raw V3 probe failures remain in private historical evidence only.

## In-app navigation

These additional checks clicked Search / filter records from a filtered dashboard and compared the complete destination query after the in-app route settled. They supplement the primary timing matrix without replacing its measurements.

| Case | Transition | Time | Result |
| --- | --- | ---: | --- |
| dashboard-tokens-02 | Search / filter records | 0.20 s | Query preserved |
| dashboard-human-02 | Search / filter records | 0.15 s | Query preserved |
| dashboard-parallelism-02 | Search / filter records | 0.34 s | Query preserved |
| dashboard-tools-02 | Search / filter records | 0.28 s | Query preserved |
| dashboard-mcp-02 | Search / filter records | 0.18 s | Query preserved |
| dashboard-skills-02 | Search / filter records | 0.17 s | Query preserved |
| dashboard-carbon-02 | Search / filter records | 0.20 s | Query preserved |
| dashboard-time-02 | Search / filter records | 0.16 s | Query preserved |
## Tooling validation

- `npm --prefix web run check`: passed: typecheck, schema/recipe/bundle checks, and 564 unit tests.
- `go test -short ./...`: passed (Go test cache).
- Usage, MCP, Tools and Skills browser structure/focus checks: 72 passed, zero failures or skips; includes desktop and phone layouts.
- `node --check` on all three audit scripts: passed.
- `git diff --check`: passed.
