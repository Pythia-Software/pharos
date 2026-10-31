# Thinking dashboard performance investigation

Measured on October 30, 2026 using the October 28, 9 PM euclid backup,
through an isolated APFS dev-library clone. The original backup and production
library were untouched. Index migrations and normal dev maintenance ran only on
the clone. API benchmarks and the standalone profiling probe are read-only.

The real population contained 1,115,135 retained requests, 792,747 usable gaps,
31 models, 132 model/effort groups and 447 daily periods. No sampling, shortened
history or top-N limits were introduced to make these measurements faster.

## Method

Serial HTTP requests covered Overview, daily distributions, model heatmaps,
effort histograms, model/effort wait and reasoning boxes, a selected week in
six-hour buckets, raw and adjusted calendars, a single model, and validation.
Additional cases used native date, model and derived-gap predicates. Response
packets and timings were saved under `.context/thinking-performance/`.

A tagged Go probe separated record reconstruction, fits, aggregation, compaction
pairing, traces and JSON encoding, with CPU and allocation profiles. A browser
run replayed the saved real response packets to separate chart rendering from
server latency. These chart measurements use real data, not synthetic fixtures.

Before changes, response-to-paint took approximately 89–191 ms for the five
sampled views. HTTP requests took 15–34 seconds, and validation hit the 120-second
budget. The standalone Overview profile spent 29.61 seconds reconstructing
records, 3.84 seconds fitting and less than a second aggregating/pairing/tracing.
About 77% of sampled CPU time was in SQLite page I/O through system calls.
JSON encoding took about 7 ms. The main problem was server work repeated for
each control/view change.

## Changes

- Share one immutable qualified population per catalog generation. Concurrent
  misses share the same build. Model, effort and range selections run against
  that population in memory, preserving pre-filter predecessor evidence.
- Cache dropdown metadata, fits, native filter ID selections and numeric
  validation results. The three variable-result families each retain at most
  four completed cohorts, expire after five idle minutes, and release closures
  holding request slices. Only the fixed population/options are background-warmed.
  Source changes, including external writes, invalidate results through the
  existing generation monitor.
- Keep normalization off shared records. Cached fit identities include input
  values, not just request IDs, so overlapping source updates cannot seed a fit
  for changed observations. Serialize fit computation per catalog.
- Use record indexes when grouping for aggregation, compactions and traces,
  avoiding repeated copies of large request structs. Preallocate the initial
  source slice from the ledger count; the capacity hint never limits results.
- Scan only the request columns timing needs and preserve request-index order.
  Covering request/conversation/tool indexes avoid wide row reads. A partial
  metadata index avoids unrelated messages. Query-local scratch storage gets a
  bounded 64 MiB cache and retains disk spill; original connection settings are
  restored on release.
- Native date/model/token predicates select IDs directly from the ledger rather
  than reconstructing timing. Derived timing predicates retain the authoritative
  SQL projection and reuse their selected IDs on repeat requests.
- Validation reuses qualified request identities and reads each selected metadata
  body once, sharing a narrow scalar relation between timers and counters. JSON
  booleans and strings remain excluded from numeric measurements.
- Expose `Server-Timing` phases for subsequent diagnosis in browser/network tools.

The profiled standalone Overview workload allocated about **2.7 GB instead of
7.6 GB** over its execution. These are cumulative allocations, not resident or
peak memory. The shared population does retain a substantial full-library
in-memory representation while the catalog is open.

## Measurements

The initial optimized serial run and its warm repeat showed:

| Query | Original HTTP | Optimized, population loaded | Warm repeat |
| --- | ---: | ---: | ---: |
| Daily distribution, all requests | 29.44 s | 0.56 s | 0.48 s |
| Model heatmap | 29.66 s | 0.36 s | 0.31 s |
| Effort histogram | 32.48 s | 0.35 s | 0.32 s |
| Model + effort wait boxes | 29.47 s | 0.55 s | 0.53 s |
| Model + effort reasoning boxes | 28.07 s | 0.56 s | 0.50 s |
| Selected week, six-hour/model buckets | 14.76 s | 0.08 s | 0.07 s |
| Calendar, raw, weekly rows | 30.34 s | 2.78 s, including first fit | 1.07 s |
| Calendar, adjusted, monthly rows | 30.74 s | 1.31 s | 1.01 s |
| One-model effort histogram | 15.71 s | 0.06 s | 0.04 s |
| Overview | 33.78 s | cold load, see below | 1.64 s |

All ten successful original response packets were numerically identical in the
final run after normalizing group/effort-list ordering. Counts, quantiles,
factors, calendar estimates, compaction pairs and traces were unchanged. Ordering
is now deterministic and keeps Unreported last. The original validation request
timed out, so it has no original result packet to compare. The final one-scan
validation result matched the earlier optimized two-scan implementation exactly:
14,519 matched timers, 252 counter reports and 41 counter-overlap reports.

A final pass of all **14 query cases** on the final implementation, followed by
an identical warm repeat, gave these additional measurements:

| Query | First evaluation after population load | Repeat |
| --- | ---: | ---: |
| Validation for selected week | 22.97 s | 0.10 s |
| Native date predicate | 0.60 s | 0.16 s |
| Native model predicate | 0.60 s | 0.12 s |
| Native derived gap ≥ 30 seconds | 50.79 s | 0.12 s |

In this more contended final run, warm daily/group distributions took
0.37–0.73 seconds, the single-model histogram 0.07 seconds, selected-week buckets
0.13 seconds, calendars 1.50 seconds and Overview 2.19 seconds. The cold Overview
was 90.71 seconds (81.92 seconds reading records), followed by consistently warm
requests. Results matched across the final first/repeat packets.

Live browser navigation, including API wait and initial page rendering, took
2.57 seconds for Overview, 0.96 seconds for Detail and the 132-group heatmap, and
1.45 seconds for the adjusted calendar. All four rendered without page errors or
horizontal overflow at 1440 × 963. Screenshots are saved locally under
`.context/thinking-performance/live-{overview,detail}.png`; the annotation tool
remains available. Response-only render replay remained about 89–196 ms.

Cold behavior must be distinguished from these improvements. An isolated
optimized full Overview profile took **27.65 seconds**, including **23.43 seconds**
reading records. An HTTP cold load under concurrent dev maintenance/test load
took **81.50 seconds**. These are not a controlled cold/warm speedup comparison;
they show the remaining sensitivity to database and machine contention. Most
views then returned in well under a second using that same population.

A temporary-table experiment for grouped tool evidence took 37 seconds for the
profiled workload and did not demonstrate a gain; it was removed. The checked-in
implementation retains the smaller changes with demonstrated benefits.

## Remaining costs and tradeoffs

- The population cache is process-local. Startup and source invalidation still
  require a full reconstruction; narrower dashboard ranges currently do not
  eliminate this initial scan. The shared generation also invalidates on changes
  to some inputs unrelated to Thinking. A persisted incremental timing projection
  and narrower dependency tracking are the next substantial cold-load options.
- A new native predicate on derived timing can still take tens of seconds on
  its first evaluation. Cached selections help subsequent views of that same
  population; they do not turn arbitrary SQL into an indexed query.
- A first metadata validation scan remains I/O-heavy. It is lazy and cached;
  it no longer rebuilds the full request/tool projection for each measurement.
- The first calendar for a new cohort fits held-out estimates; subsequent
  row/measure changes reuse them. Calendar aggregation and copying ratios onto
  request-local records still cost more than simple distributions.
- Index creation has a one-time upgrade cost. On this 104 GiB dev catalog,
  conversation/request indexes built in approximately 0.5/13 seconds, while the
  partial metadata index took about 157 seconds because it must examine the
  existing message table. These are migration costs, not per-query timings.
- Cache limits bound completed cohorts, while in-flight callers retain their
  own snapshots. Cancellation does not poison the shared cache; shared builds
  have an explicit two-minute budget. This remains exact computation rather
  than approximation.

## Reproduction and checks

The read-only probe requires a `.pharos-dev` marker and never initializes or
migrates the supplied catalog. Stop competing benchmark work before comparing
profiles:

```sh
PHAROS_THINKING_PROBE=/path/to/dev-library/catalog/catalog.sqlite3 \
  go test -tags probe -run '^TestThinkingPerformanceProbe$' -v \
  -o .context/thinking-performance/archive.test \
  -cpuprofile .context/thinking-performance/cpu.pprof \
  -memprofile .context/thinking-performance/heap.pprof ./internal/archive
```

Set `PHAROS_THINKING_PROBE_QUERY` to an API query string for another view/range,
including `evidence=1` for validation. Normal tests cover cached/direct population
parity, millisecond timestamp boundaries, native compound predicates, external
source invalidation, validation freshness, fit immutability and bounded fit
cohorts. Existing statistical, eligibility and identity tests remain in place.

Final checks passed: `go test -short ./...`, the targeted Thinking/older-snapshot
fixture tests, six Thinking Node tests, probe-tag compilation, response parity
for all ten successful baseline cases, and identical responses across all 14
final first/repeat HTTP cases. The older-snapshot fixture now removes the new
harness-dependent index when constructing its historical schema. No frontend
source or Query Table dependency changes were needed for this performance work.
