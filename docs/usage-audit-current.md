# Usage audit

Status: **Complete**. Updated 2026-10-21T06:38:38.795Z. 432 completed cases across 17 pages. Selected runs: `repair-full-03`, `repair-final-affected-01`.

Primary views: **351 Green, 81 Yellow, 0 Red, 0 Black**. 0 cases returned an error or failed to settle. Fast error responses still count as errors.

## Method

The audit drives the real Chrome UI and service against the existing isolated development library. It runs serially, intercepts preference writes inside each browser context, and leaves the source library and saved settings unchanged. Each case varies valid dates, categories, keywords, numeric/null/OR filters, grouping, formulas, sorting, or dashboard controls. Empty results require a completed response. Fresh-context URL replays use different saved display preferences to detect hidden state.

Timings include data requests and rendering. A case uses the slowest of initial navigation, changed-control completion, and URL replay. **Green <1 s; Yellow 1–<4 s; Red 4–<30 s; Black ≥30 s without a settled result.** Errors are counted separately from latency. Linked queries have their own timing summary below. These are local measurements with warm service caches, not guarantees for other hardware or cold catalogs.

For views that settle, every distinct dashboard query/record link is executed. Up to five representative outgoing links per dashboard also open in fresh browsers. Equivalent queries and records/search links reuse a native execution within the frozen snapshot, including its original failure if it timed out. Native grouped results inspect a source group when one exists. Query pages check their dashboard return link. This samples the large combination space; it does not prove all possible queries work.

The complete canonical query is compared with the resulting URL: source predicates, columns, sorting, limits, offsets, metrics, grouping, and HAVING. Singleton-OR normalization is equivalent. Default, reduced-column, and paged Tool summary and Skill evidence cases also check row actions. Additional targeted cases cover chart-only legend choices, expanded series, and Parallelism selection/zoom. Passive distinct-value suggestions do not gate readiness.

432/432 cases completed fresh-context replay. A timed-out primary view can block replay and outgoing checks; those unexercised checks are not passes.

Error counts include failed filter-choice lookups (`/distinct`) even if the requested rows rendered. Those auxiliary failures are listed separately in the inventory. Successfully handled metric-budget fallbacks are recoveries rather than visible view failures.

72 requested control interactions completed. 26 optional interactions were unavailable in that filtered view; those cases still tested initial loading and URL replay.

## View results

| Page | Cases | Green | Yellow | Red | Black | Errors | URL issues | Link issues | p50 | p95 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Tokens & cost dashboard | 27 | 27 | 0 | 0 | 0 | 0 | 0 | 0 | 0.35 s | 0.73 s |
| Human words dashboard | 27 | 26 | 1 | 0 | 0 | 0 | 0 | 0 | 0.33 s | 0.87 s |
| Agent time dashboard | 25 | 22 | 3 | 0 | 0 | 0 | 0 | 0 | 0.29 s | 1.12 s |
| Parallelism dashboard | 27 | 27 | 0 | 0 | 0 | 0 | 0 | 0 | 0.49 s | 0.95 s |
| Tools dashboard | 25 | 16 | 9 | 0 | 0 | 0 | 0 | 0 | 0.74 s | 1.67 s |
| Pharos MCP dashboard | 25 | 25 | 0 | 0 | 0 | 0 | 0 | 0 | 0.30 s | 0.36 s |
| Skills dashboard | 26 | 26 | 0 | 0 | 0 | 0 | 0 | 0 | 0.37 s | 0.60 s |
| Carbon impact dashboard | 25 | 24 | 1 | 0 | 0 | 0 | 0 | 0 | 0.35 s | 0.63 s |
| Token usage query | 25 | 7 | 18 | 0 | 0 | 0 | 0 | 0 | 1.25 s | 1.93 s |
| Conversation activity query | 25 | 6 | 19 | 0 | 0 | 0 | 0 | 0 | 1.32 s | 1.80 s |
| Human words by conversation query | 25 | 24 | 1 | 0 | 0 | 0 | 0 | 0 | 0.57 s | 0.95 s |
| Human messages query | 25 | 25 | 0 | 0 | 0 | 0 | 0 | 0 | 0.52 s | 0.71 s |
| Tool summary query | 25 | 20 | 5 | 0 | 0 | 0 | 0 | 0 | 0.59 s | 1.08 s |
| Tool calls query | 25 | 18 | 7 | 0 | 0 | 0 | 0 | 0 | 0.68 s | 2.51 s |
| MCP calls query | 25 | 25 | 0 | 0 | 0 | 0 | 0 | 0 | 0.46 s | 0.57 s |
| Skill evidence query | 25 | 25 | 0 | 0 | 0 | 0 | 0 | 0 | 0.49 s | 0.66 s |
| Parallel activity query | 25 | 8 | 17 | 0 | 0 | 0 | 0 | 0 | 1.73 s | 3.50 s |

## Linked-query timing

Measured 359 outgoing executions: Green 344; Yellow 15; Red 0; Black 0. p50 0.14 s, p95 0.96 s. Browser transitions include a fresh context; API transitions include native query preparation and execution.

## Findings and next actions

- Performance: 0 primary views and 0 outgoing executions exceed the Yellow threshold.
- Query execution: 0 cases have errors; auxiliary metadata failures are included.
- URL completeness: 0 cases have replay or query-state mismatches.
- Cohesive transitions: 0 cases have unresolved filter or row-action issues.

Repairs and implementation details are tracked in [usage-audit-repairs.md](usage-audit-repairs.md). The inventory below is generated only from the selected runs.

## Failure inventory

No unresolved errors, URL replay mismatches, or filter-transition issues in the selected cases.

## Slow views

No primary view exceeded four seconds.

## Slow outgoing queries

No successful outgoing query exceeded four seconds.

## Reproduction and evidence

Run `node tools/audit-usage.mjs --run NAME` against the existing dev copy. Use `--pages dashboard:tokens,query:usage` to target selected pages, `--cases 25` for the matrix, `--ids dashboard-carbon-06` for a specific reproduction, and the same run name to resume checkpoints. `--controls --cases 30` includes targeted control cases. `--origin` and `--config` select another isolated service. Only one audit runs at a time. `tools/summarize-usage-audit.mjs --runs BASELINE,CORRECTIONS,CONTROLS` combines explicitly selected evidence.

Use --in-place on a selected dashboard run to exercise live in-app transitions. The selected evidence is reproduced with tools/summarize-usage-audit.mjs --runs repair-full-03,repair-final-affected-01. Optional --summary FILE and --validation FILE add the machine-readable inventory and check results.

Private manifests contain exact case URLs, sampled categories, frontend/backend hashes, and projection readiness. JSONL checkpoints contain request/transition timings and failure evidence under .context/usage-audit/<run>/. Query values and credentials are excluded from this report.

Ordinary record drilldowns execute through /rows-v2, matching the UI and its exact-search budget. Grouped drilldowns execute through native V3. Equivalent links reuse a verified execution within the frozen snapshot; browser transitions are measured separately.

The selected corrections exclude read-only Carbon labels from interactive legend checks and compare normalized predicates. A generator test caught repeated Agent time combinations; the affected cases were replaced with distinct time-bucket combinations. Completion requires at least 25 distinct initial parameter URLs on each page. Targeted control cases can share a starting URL while exercising different actions.

- repair-full-03: frontend SHA-256 `8b47c2d9bfe5f5504cda4b6199cbd09dbc9fd04b9499250f03188bd72ce47fb3`; backend SHA-256 `6e233fa34037cb4b62bc80830d9301f55091abbb73be6b3470fe7e6b86863772`.
- repair-final-affected-01: frontend SHA-256 `8b47c2d9bfe5f5504cda4b6199cbd09dbc9fd04b9499250f03188bd72ce47fb3`; backend SHA-256 `981ebf46ffaea551f668d8575aa498d6e33b06c9589e251b51999eef6da6eeb4`.

## In-app navigation

These additional checks clicked query links from filtered dashboards and compared the complete destination query after the in-app route settled. They supplement the primary timing matrix without replacing its measurements.

| Case | Transition | Time | Result |
| --- | --- | ---: | --- |
| dashboard-tokens-02 | Search / filter records | 0.16 s | Query preserved |
| dashboard-tokens-03 | Open grouped query | 0.19 s | Query preserved |
| dashboard-human-02 | Search / filter records | 0.20 s | Query preserved |
| dashboard-human-03 | Open grouped conversation totals | 0.21 s | Query preserved |
| dashboard-parallelism-02 | Search / filter records | 0.28 s | Query preserved |
| dashboard-parallelism-03 | Open grouped query | 0.36 s | Query preserved |
| dashboard-tools-02 | Search / filter records | 0.19 s | Query preserved |
| dashboard-tools-03 | Open grouped query | 0.28 s | Query preserved |
| dashboard-mcp-02 | Search / filter records | 0.17 s | Query preserved |
| dashboard-mcp-03 | Open grouped query | 0.19 s | Query preserved |
| dashboard-skills-02 | Search / filter records | 0.16 s | Query preserved |
| dashboard-skills-03 | Open grouped query | 0.17 s | Query preserved |
| dashboard-carbon-02 | Search / filter records | 0.16 s | Query preserved |
| dashboard-carbon-03 | Open grouped query | 0.20 s | Query preserved |
| dashboard-time-02 | Search / filter records | 0.14 s | Query preserved |
| dashboard-time-03 | Open grouped query | 0.20 s | Query preserved |
## Tooling validation

- go test -short ./...: passed on final16.
- go test ./...: passed on final16.
- npm --prefix web run check: passed; 569 tests plus types, schemas, recipes, and bundle verification.
- npm --prefix web run test:full: 569 passed.
- npm --prefix web run build && npm --prefix web run check-bundle: passed.
- npm --prefix web run test:browser -- --test-name-pattern='Usage|Tools|Settings|MCP': 63 passed.
- python3 -m unittest discover -s tools: 13 passed.
- node --check tools/{audit-usage,audit-usage-link-transport,summarize-usage-audit}.mjs: passed.
- git diff --check: passed.
