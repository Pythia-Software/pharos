# Data readiness: in-app prototype

The header's **Data updates** tool and the icons on shared charts, native query
tables and query cards open one read-only processing dialog. They use the normal
header tool registry (including More and Layout Preferences), the Data Guide
modal shell, a database-and-clock glyph and the activity panel's progress tracks.
The existing Background Processing page owns all schedule and run actions. Its
new Now section reuses the activity panel model, showing current work and the
same CPU/GPU, memory and I/O measurements before the existing task controls.

`DataReadinessIndicator` registers a stable result identity, dataset and current
query at the rendering boundary. The header reduces the union of the mounted
results on the active page; unmounted cards and retained hidden pages do not
contribute. Cards keep their own scope. Label fields in a query add labeling
dependencies. This uses the existing shared status stream, without another poll
or request per card. The single dialog has native focus containment, Escape and
focus restoration, including tools moved into More.

The dialog shows the known gap, last source observation, next eligible check and
reported counts/progress. A first unfinished analysis uses the existing library
empty-card motif rather than claiming a valid empty result. Unknown totals stay
indeterminate. The processing map has been removed from the prototype.
The Settings link opens the relevant existing task disclosure and highlights it, leaving the
workload and system load visible at the top before the run controls.

## Reviewing inside the app

Use regular app routes for actual processing status. Add `readiness_preview=hours`
(or minutes, recent, orange, days, clear, missing, running, paused, failed or
unknown) for explicit simulated states. Add `readiness_open=1` to open the dialog
immediately. The picker has one **Up to date** state; remove the preview
parameter to return to actual data. Preview changes presentation only, never
jobs, Settings or query results. Links and Back/Forward restore the same state.

For example: `/analyze?analyze_topic=human&readiness_preview=hours&readiness_open=1`.
The standalone design study remains at `docs/mocks/analysis-readiness.html`.

## Evidence and current limits

This is a prototype over the processing ledger, not the final per-query
freshness receipt contract. Dataset/field dependencies are centralized in
`data-readiness-model.ts`; executed-query receipts must eventually replace that
provisional declaration, including filters, joins and mixed publication. The
modal identifies this limitation. Untracked datasets, missing labeling/event
ledger evidence and disconnected status stay unknown. An older saved cover is
not cleared just because the processing ledger is current; it retains its fetch
time and refresh error until its preview is replaced.

`freshness.pending_since` exposes the oldest observed queued change where the
backend measures it, including the authorship pending queue. Delay severity uses
that evidence, never the age of the last successful publication. A planned pass
within its cadence stays distinct from known staleness. Exact delay remains
unspecified where the backend has no pending-input time. A source check also
never promises to include work created concurrently in another app.

CPU/GPU values in Background Processing are utilization measurements. Resource
contention is never a freshness icon or summary state. Shared admission limits
are not reported or enforced by this change. Task gating/dependency metadata and coherent query
publication receipts remain follow-up integration work described in the
[contract proposal](handoffs/analysis-readiness-contract.md).

## Severity and coverage

The database outline uses the shared 24px SVG sprite. A filled clock face shows
observed hours behind on a 12-hour scale: three hours is a quarter, six a half,
and nine three quarters. At twelve hours or more it stays full, while the label
keeps the exact age. Only current results replace the clock with an unfilled
checkmark. Missing results use a dash badge; unknown status or unmeasured lag
uses a question badge. Scheduled updates and older saved results never get a
current checkmark just because they have a neutral tone. The model carries the
measured delay as `lagMs`; the icon does not infer it from labels or severity.
Only the quantitative pie is filled; the rest follows `docs/iconography.md`.
The [comparison guide](mocks/freshness-icons.html) preserves the approved design.

It has no job-state or resource glyphs. Under 15 minutes is expected and neutral;
15 minutes through two hours stays neutral; more than two hours is yellow, more
than eight hours orange, and more than 24 hours red. Unknown delay is neutral.
A single shared clock advances age between status messages. Causes and work
progress belong inside the dialog; all controls remain in Background Processing.

Coverage is attached to owned rendering boundaries, rather than DOM discovery:

| Surface | Shared boundary |
| --- | --- |
| All query tables and their native metric charts, including Findings/Risky actions/Fixes | SourceQueryBuilder: run-bar action before request timing, even when the editor is collapsed |
| Library conversation results and message-label cards | Same query boundary, plus shared first-analysis empty state around all presentations |
| Catalog trend, comparison, distribution, scatter and cycle dashboards | AnalysisTrend / DashboardAnalysis |
| Bespoke Habits and Waiting/Thinking charts | DashboardResultActions, reusing each target's metadata and source |
| Parallelism ribbon and supporting conversation table | ConcurrencyPanel |
| Carbon estimate | Host boundary around the classic summary renderer |
| Token, Human Words, Conversation, Tool Time and Agent Experience summary tiles | Each independent metric's label row |
| Agent Experience rates and examples | Existing panel headings |
| Efficiency, Risky actions and Fixes dashboards | Existing page heading; the tables use the query boundary |
| Saved Home and Card Library cards | QueryCard; dashboard sources resolve through dashboardCardSource even without a query token |

Query Table provides the run-bar slot and button styling; the original request
is recorded in the [upstream handoff](handoffs/query-table-run-bar-slots.md).

Nested tables that explain one chart share that chart's scope; static methodology
and live diagnostics (sync history, request logs, activity/load and feedback
reports) do not depend on periodic analysis and do not get analysis-age icons.
Missing ledger domains still report unknown. This widens display coverage without
claiming authoritative per-result revision receipts.

## User-facing language

The indicator and dialog use the same summary: “Up to date as of 6 minutes ago,”
“3 hours behind,” “No results yet,” “Showing saved results,” or “Update status
unavailable.” A missing check timestamp stays explicit (“No updates waiting”
with an explanation that the check time is unavailable). Scheduled changes say
“Update scheduled”; they do not imply that those changes are already displayed.
“Last checked” and “Next planned check” distinguish observations from planned
work, without promising a start time. Step names describe actions (“Read tool
calls,” “Summarize tool use”), and progress reads “58 of 128 messages.” Raw job
identifiers and service diagnostics stay out of this surface. The link to the
existing Background Processing page reads “Update settings.”

## Output availability and preview isolation

Preview state is read from an explicitly supplied `readiness_preview` parameter;
the shared view hook never captures a default for this field. Ordinary navigation
and reloads keep the live status stream.

The ledger reports `output_missing` from publication metadata independently of
the changing running/queued reason. It means a required first publication is
missing, not that a query has no rows. Only a successfully fetched empty result
can use the first-build surface. Available rows and chart values keep rendering
with their update indicator. One new source is partial coverage; a wildcard
source dependency is missing only when all its sources lack output. Unknown
availability, loading and query errors never justify hiding results. A page with
mixed available and missing outputs summarizes updates instead of claiming the
whole page has no results.

Saved-result comparisons are made against each result’s own dependencies before
combining the page status. A newer writing publication cannot invalidate an
unrelated token-usage card.

Source dependencies include captured history from other Macs when the ledger
reports it. A library without captures needs no capture row; unreadable captures
remain unknown. Fixes also depends on Agent events, whose missing ledger evidence
stays unknown. Conversation rows carry their owning background `task_id` from
source configuration or recorded source kind, so whole-source imports use Other
sources for timing and Settings links. If ownership is unavailable, the prototype
leaves that task and its schedule unspecified. Capture rows carry `task_id`
Preserve originals, which indexes every Mac's captures; when this Mac's Other
sources task also reads the source in full, `task_ids` lists both and the modal
uses whichever has the earlier planned run, since only the schedule knows which
comes first.

Bespoke chart indicators resolve dataset IDs through the same card-source reader
as saved cards, rather than using schema names. Waiting results depend on the
versioned conversation ledger as well as ingestion. Its pending work links to
Library upgrade; Tools & usage only owns the rollups.

Query-card owners pass the resolved dataset and query used by their previews.
Saved durable links therefore retain readiness details even without a local
query-snapshot cache entry. URL decoding is a fallback for cards whose source
has not already been resolved; an unresolved source remains unknown.
