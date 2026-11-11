# Thinking dashboards

Analyze → Dashboards includes **Waiting**, **Compaction** and **Thinking**:

- `/analyze?analyze_topic=thinking-overview&analyze_data=model_requests`
- `/analyze?analyze_topic=compaction&analyze_data=compactions`
- `/analyze?analyze_topic=thinking-detail&analyze_data=model_requests`

These pages read the current library, with no study snapshot or backup embedded.
The earlier `.context/think-time` study remains separate. Timing is an observed
post-tool gap, a coarse proxy for the wait before the next model step. It is
neither verified request-send-to-first-token latency nor complete turn duration.

## Page layout and navigation

Overview contains coverage and quantile summaries, per-model cumulative wait
curves and a compact adjusted factor table. A supporting disclosure compares
waits around compaction; compaction counts have their own dashboard.
Compaction ratios pair a plot with summary cards for waits, context, output
and event counts, alongside a worked median-wait example.
Model/harness coverage, conversation traces, validation and methodology use
URL-addressable `ViewDisclosure` accordions and the shared dashboard disclosure
style. Detail links to Overview and opens
the methodology accordion through semantic reveal. Tokens and Cost, Tool Time
and Parallelism link to Waiting.

Detail has six views: distributions over time, comparison heatmap, histograms,
positive reported reasoning boxes, wait boxes and time-of-day calendar. Controls
include model, effort, thirteen breakdowns (including speed, model + speed and model + effort + speed), share/count,
median/p95/volume ordering, range and custom time buckets. Range presets choose
the same readable buckets as other dashboards (90 days uses days). Charts expand
with the page so scrolling over them scrolls the page.
Calendar adds day/week/month rows, raw/adjusted measure and paired three-hour
window comparisons. Percentile overlays can be toggled. Effort ordering remains
low to high with Unreported last, regardless of numeric sort. All observed
groups stay available; SVG facets render as they enter the viewport.

`thinking` is validated JSON in the shared view-state contract. Agents link
to these pages with `site_link` options and `days` like any other dashboard;
the site map lists each control as a key of `thinking`
([site map](site-map.md#nodes)). Partial Card
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

The Card Library's **Waiting and Thinking** topic has six recipes. Wait
distribution and timing coverage open Waiting (timing coverage with its
coverage accordion open); context compactions open Compaction; wait by
model/effort, wait by preceding tool and reasoning by effort open Thinking in
their view and breakdown. Their editable
native source query remains available for collection and preview. Native compact
previews use Query Table automatic histogram bins / Tukey boxes; the full page
uses shared rounded edges / p10–p90 whiskers. This difference is disclosed.
Previews read the stored timing projection (with a 2-million-row population
budget), and each card with a distribution is its own request, because Query
Table evaluates each distribution in a separate pass. A 7-day preview takes
about 1–2 seconds per card. Windows of 180 days or more are bound by Query
Table's distribution executor.

Speed breakdowns are available on both Waiting and Thinking, and saved cards
retain the selected breakdown. Ingestion stores normalized `speed` (`fast` or
`standard`), the original normalized `service_tier`, and `speed_source` on
messages and model requests. Explicit `usage.speed` wins (including Claude fast
mode with a standard service tier). Otherwise priority, fast and ultrafast
tiers are fast (`fastTiers` in `request_speed.go`), and all other, null or
missing tiers count as standard. A change to that list re-derives, once, only
the conversations whose stored requests took their speed from a tier it now
judges otherwise. `speed_source` distinguishes
`speed`, `service_tier`, and the `assumed_standard` fallback policy.

Codex turn configuration is retained per message and resets each turn. ChatGPT
metadata and canonical exports use the same normalization. Existing accounting
and typed settings are replayed by the versioned tool ledger. Older discarded
turn settings cannot be reconstructed; those requests follow the standard-speed
fallback. Model Requests exposes all three fields for filters, grouping, counts,
token sums and distributions, independent of these dashboard views.

## Data and execution

`thinking_table.go` projects the existing `model_requests`, linked `tool_calls`
and conversation/source ledger. It computes prior response evidence before
applying dashboard filters. The projection is stored, one narrow row per request,
in `model_request_timings` (keyed by conversation, with a covering
`requested_at` index); excluded conversations are stored in
`model_request_exclusions`. Triggers on every input mark the affected
conversations in `model_request_timing_dirty` (or flag the exclusions stale) in
the source transaction. Reads take stored rows for clean conversations and
derive dirty ones live, so results are always current. The Tools stage
(`analysis.tools`, after the rollup) refreshes dirty conversations 200 at a time
in the background, and a read that finds work pending starts that stage.
Validation reads `model_request_evidence`, the numeric timer and counter fields
of metadata messages, kept by triggers and backfilled by the same stage; until
the backfill finishes it reads the messages directly. Explicit native-alias families choose one transcript,
preferring native/non-mirrored evidence; inactive group members are excluded.
Unlinked copies cannot be deduplicated safely by text similarity.

A usable gap is response-evidence time minus the latest incoming tool-result
time, from 100 ms through 600 seconds inclusive. Missing/incomplete clocks,
cross-stream links, stale anchors and nonpositive gaps remain retained requests
with a timing-quality reason. Missing timing never becomes zero. Model Requests
is also a native query dataset for inspecting these exclusions.

The dashboards read the Model Requests dataset through
`POST /api/query/model_requests/query-v3` (`web/src/thinking-query.ts`): the
population and its counts, each group's retained requests, usable gaps,
conversations and exact-linear percentiles (Query Table quantiles: p10, q1,
median, p75, p90, p95), wait bands (`gap_bin`, labelled `0.1–0.2 s` … `500–600 s`
on the edges above), the
distributions-over-time periods (calendar `every` buckets in the viewer's zone,
one `PERCENTILE` metric per drawn line, since date grouping takes scalar
metrics), the
Waiting curves (101 quantiles) and its coverage by model and agent app, and the
Model and Effort choices. A `timezone` parameter reads the calendar fields in
the viewer's zone (`pharos_zoned_time`); without one they use the service's.
Missing provider, agent app, repository, project and machine labels are one
missing bucket, shown as Unreported (No project for projects). A value spelled
like that name (a project called "No project") is a group of its own, keyed
with a leading NUL and shown in quotes, on both sides. These answers
go through the result cache like other query-v3 reads.

Each view is one metric request of 9–19 metrics, which Query Table (1.0.7 and
later) answers in one read of the population. That read needs a source of
stored values only, so while no conversation awaits a timing refresh and the
exclusions are current, query-v3 reads `model_request_timings` directly
(`thinkingQueryDataset`); otherwise it reads `thinkingSource`, which derives
the marked conversations again, with the same answers more slowly. The
choice is made before the query's transaction, so a query on the stored
timings checks them again inside it and, if a conversation was marked in
between, is read again from the derived source. Blank
labels are stored as missing ones, so the label fields are plain columns, and
the wait band is an enum, since the shared read groups only text, enum and
boolean fields. Metric-only requests on Model Requests carry no host population
counter, which would also turn the shared read off. The shared read groups
only stored text, enum and boolean fields, so the breakdown by agent depth (a
number) reads the population once per metric: about 17 s for all history on
the replica (measurements below), where other breakdowns take 2–3 s
([handoff](handoffs/query-table-shared-scan-groupings.md)). Hour buckets divide a
day (1, 2, 3, 4, 6, 8, 12 or 24 hours) and day buckets count at most 1,000
days, as Query Table's `every` supports; older links read as the nearest.

`GET /api/thinking` keeps what needs whole conversations in order: the
workload fit's supported estimates by time of day (cells, three-hour bands and
paired comparisons), the Waiting factors, compaction pairs and traces, and
validation. It also gives the time-of-day view's raw cells (median wait,
timed requests and conversations per row period and local hour, shown when a
cell has 20 waits from 3 conversations) beside the fitted ones. Query Table
takes one calendar key per metric, and the zoned day and hour are computed
columns, which its shared read cannot use: as query-v3 metrics those two
cells alone took 17.5 s on the replica, against under 2 s for the rest of the
view. It is read-only, cancellable and has a two-minute execution
budget for fits. The views that join it (time of day, the Waiting overview)
read query-v3 live rather than from the result cache, and send both reads the
same `now`, so a Today or rolling Range ends at one instant and both describe
the same requests. The page merges its groups onto the query-v3 groups by key;
`testdata/thinking-group-keys.json` pins both sides' keys. Covering timing
indexes avoid reading conversation/tool bodies and sorting wide request rows.
Evidence joins stream in request order and the server scans directly into
numeric records, avoiding a full-library intermediate map collection. A leased
64 MiB SQLite scratch cache retains disk spill and restores connection settings
afterward. Counts and quantiles use all selected observations. Timing edges
repeat 1, 2, 3, 5, 10 across decades. Requests exceeding 5,000 time buckets fail
visibly and ask for a coarser interval. No top-N truncation is applied to
groups.

All dashboard views share one immutable full-library request population through
the existing catalog-generation cache. Concurrent misses share a build; source
commits, including external ledger/identity/origin changes, invalidate it. Normal
model/effort/range controls select that population in memory. Native predicates
select IDs from the stored projection. Native selections,
fits and numeric validation results each retain at most four completed cohorts,
expire after five idle minutes, and are rebuilt on demand. Completed entries
release closures holding request slices. Fits run one cohort at a time per
catalog, fitting models in parallel, and include all fitted inputs in the cohort
identity. The Overview's compaction and trace sections are computed concurrently. Ratios are attached only to
request-local copies, so calendar normalization cannot contaminate another view.
The fixed population can be warmed after a source change.

`Server-Timing` reports records, filtering, fitting, aggregation and optional
validation phases. The in-memory population is rebuilt from the stored
projection in about 5 seconds on a 1.3-million-request library, after startup or
a change to any cached-data dependency (the shared catalog generation); the warm
loop rebuilds it in the background when it was used recently. See
[the performance investigation](thinking-performance.md) for real-library
measurements and remaining costs.

The fits and SVGs are host-owned. Counts, percentiles, periods and curves,
native evidence queries and compact cards use published Query Table 1.0.7 APIs. No local package
patches, generated-bundle edits or dependency replacements are used.

Measured 2026-11-11 on a replica of a production catalog (1.43 million stored
requests; a 30-day range holds about 297,000), warm, with stored timings
current. Each view's answer matched `/api/thinking` from before the move
exactly, except the calendar's ratio view: its groups' wait-band counts there
counted fitted requests and now count timed ones, as every other view does
(the calendar does not draw them).

| view | before (`/api/thinking`) | now |
|---|---|---|
| Time, daily periods, 30 days, by model | 42 s cold, under 1 s cached | 2.5 s |
| Time, 6-hour periods, 7 days | 49 ms | 1.6 s |
| Time, weekly, all history, by model and effort | 0.8 s | 4.3 s |
| Histogram or heatmap, all history, by agent app, provider, machine, project or preceding tool | 0.4 s | 2.5–2.6 s |
| Histogram, all history, by agent depth | 0.4 s | 17 s |
| Reasoning, Wait box, Histogram, 30 days | 0.2–0.4 s | 1.6–1.7 s |
| Calendar, 30 days or all history | 0.4–1.6 s | 1.8–3.3 s |
| Waiting, 30 days or all history | 0.9–1.8 s | 1.8–3.4 s |

`/api/thinking` answered from a population it kept in memory once built; the
query-v3 answers read the catalog each time, through the result cache. While
conversations await a timing refresh, query-v3 reads the derived source, which
reads the population once per metric: 16 s to over 30 s per view in the
measurements before the stored source, until the refresh completes.

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

## Compaction counts

Compaction uses the shared catalog chart for time trends, line/bar displays,
group comparisons, shares and calendar cycles. Its default is compactions per
day by human, machine or unknown trigger. Optional breakdowns include service,
trigger + service, agent/harness, model, repository, channel, initiating machine,
execution machine and trigger evidence. Filters, range and presentation use the
shared `analyze_chart` / `analyze_filters` URL contract; chart selections open
the matching boundary records and their source messages. Create card uses the
same controls and naming functions as the chart.

`compactions` reads `compaction_boundaries`, one row per retained metadata
boundary message with its trigger and trigger evidence already classified.
Triggers on `messages` keep it current in the writer's transaction, so queries
never parse message bodies. A new catalog is complete at once. An older catalog,
or a change to the classification (`compactionBoundaryVersion`), refills it in
the Tools stage 200 conversations at a time; until that finishes, queries derive
the same rows from the messages, and a read that finds it pending starts the
stage. Consecutive
boundaries and boundaries with no later model request count separately. Claude's
`compactMetadata.trigger` records manual or automatic; Codex's attributed
`compaction_trigger` is inferred by the adapter from turn position. Unknown
triggers stay separate. Unattributed replayed Codex events and inactive
conversation-group versions are excluded. Each explicit native-alias family
contributes one active transcript with boundary evidence, preferring native
sources, then the most recorded boundaries, then a stable conversation ID.
This selection precedes chart filters and does not depend on tool calls or
model requests; unrelated conversations in a mirrored workspace remain visible.
Missing timestamps remain available in unbounded comparisons but cannot appear on a time chart. Service
means the recorded provider, while harness and channel are separate dimensions.

The Card Library offers `compactions-by-trigger` on this event population.
The retired `thinking-compactions` recipe and previously saved cards remain
readable as the legacy request-based proxy; Waiting offers only its wait card.
The existing `thinking-overview` route now displays Waiting, preserving links.
