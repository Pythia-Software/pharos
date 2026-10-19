# Usage performance audit

Measured on 2026-10-18 against an isolated APFS copy of a large development
catalog. The original catalog and running application were not modified.
The benchmark copy contains about 48,000 usage ledger facts, 33,000 daily
usage rows, 19,000 workspace groups and 1.5 million raw tool calls.
Parallelism uses a captured 14-day timing snapshot with 2,439 conversations
and 1,342 roots. These are local measurements, not cross-machine guarantees.
These timings predate the Query Table v0.8.0 merge; the backend performance
patches were carried forward, with the correctness checks rerun after merging.

## Scope and measurements

`tools/usage-bench.cjs` drives the real Usage UI. It exercises Machine Tokens
chart presets, table presets, metrics, breakdowns, intervals, date presets,
scales, smoothing, activity periods and representative repository/provider/model
filters; Human Words conversation/message sort and metric presets and chart
controls; Carbon chart options, repository/provider choices and estimate
assumptions; Machine Time periods, groupings, measures, command/activity
drilldowns, raw-call pagination and example data; and captured Parallelism's
counting modes, views, turn gaps and example data.

Actions include the table's edit debounce, request completion and two animation
frames. Request measurements cover response completion. Actions with no network
request measure browser work. The sweep explores each predefined control and
representative data-dependent drilldowns; it does not enumerate every custom
query or Cartesian combination of filters.

| Case | Before | After |
| --- | --- | --- |
| Parallelism worst Sessions / Model activity computation | 30.6 s | 273 ms |
| Parallelism real-data page navigation | Unresponsive during computation | 649 ms |
| Common token group presets | 2–4.7 s | About 0.5–0.8 s |
| Work preset, about 19,000 groups | Failed the 10,000-group limit | About 1.5–1.8 s |
| Machine Time first published-cube summary | About 9.8 s | About 0.6 s |
| Machine Time warm published-cube summary | Several seconds for large windows | About 0.1–0.3 s |
| Machine Time 90-day previous/current summaries, same fresh catalog | 7.4 / 14.0 s | 104 / 286 ms |
| Raw command-family drilldown pages, sampled families | 2.4–8.4 s | 13–131 ms |
| Parallelism final full-sweep navigation / controls | Previously blocked by layout work | 360 ms / 191–546 ms |

The final sweep completed **269 actions and 349 requests**, with no browser,
action or HTTP errors. **348/349 requests finished under one second**; Work was
the exception at 1,513 ms. Complete timings and statuses are in
`.context/usage-audited-benchmark.json`. Prior runs and a longer drilldown stress
run are retained in the same ignored directory. The before/after range above summarizes those runs, rather
than claiming every first load is identical.

## Changes

- Parallelism sweeps only active counting units, uses constant-time rank lookups,
  and repacks when active membership changes. Adjacent equivalent segments are
  merged for the UI, preserving identity, running state, placement and zoom
  behavior. Collinear SVG vertices and closed conversation-table DOM are avoided.
- Pages mount on their first visit; hidden query surfaces defer capability and
  computed-definition loads. This reduces competition for browser connections
  and catalog readers during Usage navigation.
- Token and Carbon queries reuse priced base facts without resolving the complete
  conversation/delegation graph. Queries consuming lineage fields still resolve
  it. Enrichment clones maps so shared cached facts remain immutable. Covering
  indexes avoid reading wide conversation/session rows for ledger metadata.
- The local Query Table Go backend retains v0.8.0's validation and exact
  aggregates while inlining inexpensive SQL guards, reusing validated staged
  values, fusing scalar group summaries, and materializing representatives once.
  Fused outputs are used directly without rewriting every group per summary.
  Usage alone accepts up to 100,000 groups. See
  `third_party/query-table-go/README.pharos.md` for provenance and maintenance.
- Work uses `MAX(title)` because every fact for a workspace has the same current
  workspace title, avoiding representative-row sorting for that preset.
- Machine Time chart grouping appends facts instead of repeatedly copying whole
  groups, and memoizes totals and breakdowns between unrelated table updates. Analytics
  table rows retain one drilldown identity instead of duplicating all source facts.
- Machine Time reduces published cube rows to compact additive dimensions, then
  classifies each distinct raw command identity once. Metrics reuse grouping
  populations. The cache follows cube publication as well as catalog generation.
  Unsupported filters/measures use the existing SQL implementation.
- Whole-local-day timestamp ranges and raw-call page counts use cube facts only
  when ledger generation, dirty partitions, full rebuild revision, local day and
  timezone all match in the same read snapshot. Otherwise they retain raw SQL.
- Versioned expression indexes and native candidate restrictions accelerate raw
  call filtering by command family and activity. Classification changes must bump
  the index version. Broad drilldown counts reuse fresh compact facts.
- Human Words scalar message totals share one SQL scan. Message pages sort narrow
  identities before loading message bodies. Display text is bounded after full
  source filtering/paging, so the Least typed preset accepts long pasted messages
  while filters can still match text beyond the display limit.

## Remaining limits

The goal is not fully met for every case. Work still takes about 1.5–1.8 seconds
on this catalog, despite becoming usable and avoiding repeated reductions. First
Human Words loads can be around one second; cold token fact preparation can take
1–2 seconds depending on disk/cache state and concurrent activity. Warm token
navigation is around 0.7 seconds. Restoring the complete all-time Machine Time
view after a drilldown can still take about 1.1–1.6 seconds in the browser, even
when its requests complete in less than one second. Unusually large custom
queries remain bounded
by the normal query budgets.

A stale or incompatible Machine Time cube cannot replace raw timestamp queries.
The initial snapshot had 10,441 newer non-mirrored calls missing from its published
cube; using it for 90-day timestamp queries would have changed results. Freshness
regressions now prevent that substitution. Raw fallback can still take seconds
until background rollup publication finishes. Rebuilding the isolated benchmark
copy took about 29 seconds; that background maintenance cost was not removed. Building the two new raw-call
indexes on this copy took about 27 seconds once, during catalog initialization.
Existing calendar-day/all-time cube queries continue to read their published
snapshot while maintenance is pending, matching their established behavior.

## Verification

- Full Go suite, frontend unit suite (315 tests), TypeScript and bundle checks.
- Browser integration checks for Machine Activity, Usage presets, Human Words
  messages and Carbon (12 tests).
- Query Table backend tests, excluding two upstream tests whose external schema
  fixtures are absent from the published module zip; see its fork README.
- Race checks over the affected backend paths and `go vet ./...`.
- Real-data result comparisons normalize bucket order and ignore execution/clock
  metadata, retaining row identities, totals, numeric results and diagnostics.
  Fresh-catalog Machine Time comparisons cover every period, including both
  90-day windows. Full all-time command buckets were also compared.
- The message browser check opens its existing collapsed table-layout editor
  before inspecting native filter chips; the stale assertion failed with the
  unchanged baseline bundle as well.
- Regression coverage includes lineage dependency selection and cache immutability,
  weighted cube totals, nulls/zeroes/empty populations, literal whitespace and
  `"null"` command names, publication invalidation, stale cube guards, fused group
  summaries, scalar message summaries and long-text filtering/truncation.
- Follow-up HTTP cases cover selected lineage fields with filters and sorting,
  including complete legacy rows when selection is omitted. Six browser cases
  cover repository/model links from Home, Usage and direct work URLs, both before
  Library's first mount and after returning to it. Link filters wait for table
  restoration so restored state cannot overwrite the requested filter.

## Reproduction

Only point probes at an isolated development catalog with a `.pharos-dev`
marker in the catalog's parent directory. The probe opens it read-only, disables
background maintenance, and does not initialize/migrate it. Normal application
startup creates the new indexes; a manually copied probe database needs those
indexes prepared separately to reproduce indexed timings.

```sh
npm install --prefix .context/browser-tests playwright --no-audit --no-fund

PHAROS_USAGE_PROBE=/path/to/dev/catalog/catalog.sqlite3 \
PHAROS_USAGE_SERVER=127.0.0.1:8859 \
PHAROS_USAGE_ASSETS="$PWD/internal/archive/assets" \
PHAROS_USAGE_TIMING=/path/to/captured-timing.json \
go test ./internal/archive -run '^TestUsagePerformanceServer$' \
  -v -count=1 -timeout=120m
```

In another terminal:

```sh
node tools/usage-bench.cjs --url http://127.0.0.1:8859 \
  --output .context/usage-audited-benchmark.json

# Repeat only Machine Time and Parallelism after a focused change:
node tools/usage-bench.cjs --url http://127.0.0.1:8859 --section time \
  --output .context/usage-time-final.json
```

`PHAROS_USAGE_TIMING` is optional and enables the captured Parallelism path.
The benchmark uses installed Chrome on macOS and otherwise Playwright Chromium.
It samples three arbitrary command families and all built-in activity categories;
recorded program/tool names can number in the thousands.
`PLAYWRIGHT_MODULE` can identify another Playwright installation.
For a normally authenticated service, set `PHAROS_BENCH_TOKEN` in the environment;
the benchmark never records the token or response message text.
