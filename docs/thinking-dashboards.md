# Thinking dashboards

Analyze → Dashboards includes **Waiting and Compaction** and **Thinking**:

- `/analyze?analyze_topic=thinking-overview&analyze_data=model_requests`
- `/analyze?analyze_topic=thinking-detail&analyze_data=model_requests`

These pages read the current library, with no study snapshot or backup embedded.
The earlier `.context/think-time` study remains separate. Timing is an observed
post-tool gap, a coarse proxy for the wait before the next model step. It is
neither verified request-send-to-first-token latency nor complete turn duration.

## Page layout and navigation

Overview contains coverage and quantile summaries, per-model cumulative wait
curves, a compact adjusted factor table, and paired compaction comparisons.
Compaction ratios pair a plot with summary cards for waits, context, output
and event counts, alongside a worked median-wait example.
Model/harness coverage, conversation traces, validation and methodology use
URL-addressable `ViewDisclosure` accordions and the shared dashboard disclosure
style. Detail links to Overview and opens
the methodology accordion through semantic reveal. Tokens and Cost, Agent Time
and Parallelism link to Waiting and Compaction.

Detail has six views: distributions over time, comparison heatmap, histograms,
positive reported reasoning boxes, wait boxes and time-of-day calendar. Controls
include model, effort, ten breakdowns (including model + effort), share/count,
median/p95/volume ordering, range and custom time buckets. Range presets choose
the same readable buckets as other dashboards (90 days uses days). Charts expand
with the page so scrolling over them scrolls the page.
Calendar adds day/week/month rows, raw/adjusted measure and paired three-hour
window comparisons. Percentile overlays can be toggled. Effort ordering remains
low to high with Unreported last, regardless of numeric sort. All observed
groups stay available; SVG facets render as they enter the viewport.

`thinking` is validated JSON in the shared view-state contract. Partial Card
Library links capture effective defaults. Dates always use the viewer's local
time zone; legacy pinned zones and minimum-sample filters are normalized when
opening old links.
`analyze_filters` carries additional native Model Requests WHERE predicates;
these intersect the explicit controls. A range change replaces simple native
date terms. Brush selections commit the source bucket's actual ISO boundaries,
including DST, to the same range used by every view/group. Double-click restores
the pre-brush range and bucket. Short selections refine the time bucket. Back/Forward and fresh-browser restoration share the same
bindings. Custom date/time editors parse and display values in the viewer’s local
time zone; whole dates end at the next local midnight, including DST.
Evidence queries preserve the originating page/presentation; returning
from an edited query uses its edited population rather than reapplying removed
model/date controls.

Four Card Library recipes link directly to these pages: wait distribution,
wait by model/effort, reasoning by effort, and timing coverage. Their editable
native source query remains available for collection and preview. Native compact
previews use Query Table automatic histogram bins / Tukey boxes; the full page
uses shared rounded edges / p10–p90 whiskers. This difference is disclosed.

## Data and execution

`thinking_table.go` projects the existing `model_requests`, linked `tool_calls`
and conversation/source ledger. It computes prior response evidence before
applying dashboard filters. Explicit native-alias families choose one transcript,
preferring native/non-mirrored evidence; inactive group members are excluded.
Unlinked copies cannot be deduplicated safely by text similarity.

A usable gap is response-evidence time minus the latest incoming tool-result
time, from 100 ms through 600 seconds inclusive. Missing/incomplete clocks,
cross-stream links, stale anchors and nonpositive gaps remain retained requests
with a timing-quality reason. Missing timing never becomes zero. Model Requests
is also a native query dataset for inspecting these exclusions.

`GET /api/thinking` returns numerical aggregates under normal API authorization.
It is read-only, cancellable and has a two-minute execution
budget for full-library distributions and fits. Covering timing indexes avoid
reading conversation/tool bodies and sorting wide request rows. Evidence joins
stream in request order and the server scans directly into numeric records,
avoiding a full-library intermediate map collection. A leased 64 MiB SQLite
scratch cache retains disk spill and restores connection settings afterward.
Counts and quantiles use
all selected observations. Timing edges repeat
1, 2, 3, 5, 10 across decades. Fine-grained requests exceeding 5,000 time buckets
fail visibly and ask for a coarser interval. No top-N truncation is applied to
groups.

All dashboard views share one immutable full-library request population through
the existing catalog-generation cache. Concurrent misses share a build; source
commits, including external ledger/identity/origin changes, invalidate it. Normal
model/effort/range controls select that population in memory. Native predicates
on ledger columns select IDs without rebuilding timing evidence; predicates on
derived timing still use the authoritative SQL projection. Native selections,
fits and numeric validation results each retain at most four completed cohorts,
expire after five idle minutes, and are rebuilt on demand. Completed entries
release closures holding request slices. Fits run one at a time per catalog and
include all fitted inputs in the cohort identity. Ratios are attached only to
request-local copies, so calendar normalization cannot contaminate another view.
The fixed population/options can be warmed after a source change.

`Server-Timing` reports records, filtering, fitting, aggregation, metadata and
optional validation phases. The first full-library build and a new derived native
predicate can still take tens of seconds; this is an in-process cache, not an
incrementally persisted projection. The cache uses the shared catalog generation,
so changes to other cached-data dependencies can also cause a rebuild. See
[the performance investigation](thinking-performance.md) for real-library
measurements and remaining costs.

The specialized domain aggregation and SVGs are host-owned. Native evidence
queries and compact cards use published Query Table 0.9.8 APIs. No local package
patches, generated-bundle edits or dependency replacements are used.

## Statistical support and limits

- Percentile overlays need 30/50/100/200 usable gaps for p50/p75/p90/p95 and five
  conversations per period. Quantiles remain exact when an overlay is suppressed.
- Wait boxes need 30 usable gaps and three conversations. Reasoning boxes need
  30 retained requests, three retained conversations and positive reports.
  Numeric zero cannot distinguish unreported reasoning from no reasoning.
- Factor fits use log context, log1p reasoning/other output, cache fraction and
  request position, absorbing stream, UTC date and effort effects. Each model
  needs 250 observations and 25 conversations; streams need three observations.
  Collinear factors are omitted. Approximate intervals cluster by conversation.
- Workload adjustment is a separate per-model ridge log-gap baseline with five
  conversation-held-out folds. Each training fold needs 200 observations and 20
  conversations, observed effort support and no numeric-feature extrapolation.
  This is a transparent linear baseline, **not the study's spline model**.
  Generated output is retrospective input, so this is not a request-start forecast.
- Calendar cells require 20 observations and three conversations. Hour profiles
  balance qualifying days rather than request volume. Comparisons pair the same
  days, need four days and bootstrap whole days with 600 deterministic resamples.
- Compaction compares three requests on either side within the same model,
  effort, stream and hour. Token comparisons use all three post-compaction
  requests; wait comparisons omit the first gap, which can span compaction,
  and require usable timing for the next two requests. The before side needs
  two usable gaps. Ratios above 1 remain
  in all statistics but lie outside the requested fixed linear 0–1 viewport.
  Workload and task stage can change with compaction; this is an association.
- Traces are examples: the twelve longest selected streams, at most the first
  1,000 requests each. Their trailing mean resets at compaction. Aggregate charts
  always use the full selected population.
- Validation loads only when opened, extracting numerical metadata fields in one
  body scan shared by timer and counter aggregation. A partial metadata index
  avoids unrelated messages. Direct
  timers deduplicate repeated response identities; cumulative API/tool counters
  use the latest conversation state. Scatterplots cap points at 1,600; statistics
  use all matched evidence. Counters can overlap, reset or span the request window.

Calibration on future dates and independent libraries is still needed before
turning factor estimates or hourly patterns into prescriptive recommendations.
No public outage correlations or per-request additive explanation are presented.

## Verification

Backend tests use disposable catalogs for timing eligibility, identity suppression,
filter/dataset parity, exact quantiles, held-out fitting, paired compactions,
calendar/time-zone bounds and timer/counter reconciliation. Node tests exercise
state validation, all view/breakdown combinations, moving card windows and evidence
navigation. Browser tests use **synthetic numeric fixtures**, not user measurements,
for real control restoration, brushing, disclosures, Back/Forward and responsive
layout at desktop/phone sizes.
