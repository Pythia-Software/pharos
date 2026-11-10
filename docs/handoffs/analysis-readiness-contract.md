# Result-scoped freshness: prototype contract

Standalone UX study: [analysis-readiness](../mocks/analysis-readiness.html). Its
counts, capacities, clock and progress are examples. A first [in-app prototype](../data-readiness.md)
now reuses the real header, cards, tables, charts, dialog styles, live status
stream and Background Processing controls. It centralizes provisional display
scopes and exposes measured pending-input age; authoritative executed-query
receipts and shared CPU/GPU admission are still not implemented. The sections
below describe the intended final contract rather than claiming that those
remaining guarantees already exist.

## The three different questions

1. **What known changes are missing?** Compare observed input revisions with
   published output revisions. If the first omitted change is known, say “at
   least 3 hours of known changes missing.” A publication timestamp alone cannot
   establish that gap, especially for historical edits and backfills.
2. **What has not been observed?** Show the last source-check time and next
   eligible check independently. “No known staleness” never promises that work
   being created concurrently has already been ingested. An unavailable status
   source, untracked domain, or missing receipt is unknown, not green.
3. **What is actually on screen?** Compare the revisions consumed by each
   displayed result with the published revisions. Completing analysis cannot
   clear the indicator on an older browser result until that result is replaced.

The prototype uses neutral through two hours, yellow above two hours, orange
above eight hours and red above 24 hours, with explicit age in the nav summary and icon tooltip. Individual
results use only the severity icon; they do not repeat the summary text. Running and queued are
orthogonal activity states: neither erases missing input. Missing first output
is different from a valid zero-row query.

## Existing foundations, checked in this repository

- `internal/archive/freshness.go` and `freshness_server.go` own domain evidence,
  real backlog counts, reasons, catching-up operations and next-run descriptions.
  `docs/background-work.md` §5 requires positive evidence for `current` and
  distinguishes `scheduled` from `behind`. Preserve that distinction: a planned
  daily reconsideration is not automatically evidence of missing records.
- `internal/archive/status_api.go` publishes the ledger, background plan and
  resources in one coalesced snapshot/stream. `assets/status-store.js` already
  shares one client copy and falls back to polling. Extend this path rather than
  creating a separate request/stream per indicator.
- `background.go` declares items, affected stages, cadence, optional capabilities,
  stage ordering, quiet/overdue policy, machine-busy and battery gates. Writing
  estimates run frequently now; historical writing refinement is a separate
  daily item. `background_run.go` distinguishes direct source indexing from
  capture-and-index of preserved files. Do not require capture before every
  analysis: the frequent Index changes path reads changed sources directly.
- `operations` progress already models totals that may be unknown. Resource
  telemetry reports CPU, memory and GPU utilization. Telemetry alone is not
  admission capacity or evidence that another task can run.
- `analysis_partitions.go` has revision tokens that prevent an old build from
  clearing inputs changed by another writer. Reuse this principle for the
  end-to-end receipt contract.
- `web/src/human-analysis-catalog.ts` uses `writing_messages`, with optional
  message-label breakdowns; `people-message-labels.ts` describes where those
  fields apply. `dataset-refresh.ts` broadcasts invalidation but does not prove
  which revisions a displayed result consumed.

## A formal boundary between processing and rendering

The processing service owns the domain/task graph, observed revisions,
publication evidence, scheduling eligibility, gate states, resource claims and
operation progress. Graph nodes and prose must be projections of these same
records. No independent UI list of “steps for page X” should become authoritative.

The query resolver owns dependency discovery. For each executed query it returns
an opaque **result receipt** with the data, including:

- library identity and query/filter fingerprint;
- actual domains read, including dependencies introduced by joins, selected
  columns, measures, filters and breakdowns;
- the output revision vector read by that query, and any incomplete/mixed
  publication state;
- result identity, snapshot sequence, and available source-observation/coverage
  bounds. Use null for evidence that was not measured.

Resolve this against the executed query, not just the page route or base dataset.
For example, adding a message-label breakdown adds labeling and query-fact
requirements to an otherwise unchanged Human Words query. Backend revisions
should be opaque tokens; the prototype's integers only simplify simulation.

A common rendering boundary registers that receipt while its result is
actually displayed. Metric cards, dashboard cards, charts and table adapters use
the same registration API. A registration has a stable result identity, scope,
receipt and accessible name; it is replaced with the data and disposed on
unmount. During a refresh the old data keeps its old receipt. A stale response
must not replace a newer query's data or receipt.

The page header reduces the **union of currently displayed receipts** against
one processing snapshot. An element indicator reduces only its own receipt.
Changing filters, showing a label breakdown, removing a dashboard card, switching
pages, and cached/offscreen results must have explicit lifetime semantics.
An unrelated stale backup must not color an analysis page. Unregistered custom
results should surface unknown coverage rather than disappear from the union.

`analysis-readiness-model.js` demonstrates this boundary: `Store.register`
replaces the mounted set atomically; `status` serves all indicators; `required`
expands dependencies; and `queue` deduplicates unfinished prerequisites. The mock
UI supplies explicit result receipts in `registerVisible`; production must get
these from the shared query/result boundary, not duplicate this hand-maintained
mapping across pages.

## Dependency graph semantics

Use typed edges, not one undifferentiated execution chain:

- **Data:** a task requires a particular input revision/output from another task.
- **Trigger:** an event requests work; manual and periodic events are alternatives.
- **Gate:** admission needs library availability, configured capability,
  automatic-mode policy, quiet time, machine load or power policy, as applicable.
- **Resource:** a task claims capacity from a constrained pool. A CPU backend and
  an MLX backend can declare different requirements. Cross-library resource
  budgets need broker ownership if they are enforced across services.
- **Execution order:** current pass ordering is not necessarily a hard data
  dependency. Show it differently so users do not conclude an unrelated stage
  has to be recomputed.

The modal trims unrelated branches for a small result lineage. All processing
expands the wider context; larger lineages retain muted unrelated nodes.
Every node is inspectable. Mermaid source and the rendered diagram come from
one node/edge declaration. The status list remains a linear, accessible way to
inspect all prerequisites and progress on a narrow screen.

The prototype graph is a relevant slice of the existing paths, not an exhaustive
inventory of maintenance, daily refinement, capture, backups or overdue bypasses.
It follows today's frequent index / writing and hourly label paths; shared CPU
and GPU admission pools are explicitly a proposed addition. Current cadence,
capability and backend settings must generate the production graph dynamically.
Do not reinterpret “10 quiet minutes” as “busy for 10 minutes.”

## Status and settings boundaries

The status modal is read-only: a short freshness headline, the source-check
interval, progress for required stages and a link to the inspectable processing
map. Counts and a single next-check/reason line replace repeated explanatory
paragraphs. A known-input gap and an unchecked source interval remain separate
facts. Completed output on an older displayed result directs the user to refresh
that result on its page.

Every analysis/schedule/run control belongs in Settings → Background processing.
The modal links there with its relevant stage highlighted. Settings shows the
whole workload, other-library resource holders and capacity before its uniform
mode controls and one-time update action. This context is intentional: requesting
fresh data has resource tradeoffs that cannot be judged from one isolated card.
The mock's Update now requests the listed analysis stages library-wide; backup
work shown in the workload is a separate maintenance path.

The graph has a sidebar entry and a direct prototype link (`#map-all`), as well
as the modal's prominent map link. It remains inspectable within the prototype;
a screenshot is not the review surface. Its manual trigger node describes the
Settings action and does not itself enqueue anything. Prototype simulation
controls also live outside the modal, in the settings page.

## Publication and enqueue behavior

From Settings, queue the union of unfinished analysis prerequisites once, with an idempotency key and an
accepted plan receipt. Reuse active work and show precisely which gate or pool
is holding it. A one-time request can bypass automatic timing/quiet policy;
input prerequisites, safety/resource admission and library availability still
apply. It must not silently resume or rewrite a paused/manual schedule.

An operation captures its input revision when it starts. If newer input arrives,
finishing that operation must not clear the newer dirty revision. The prototype
has a **New messages indexed** action while a job is running to rehearse this.
Track the first still-omitted change when advancing coverage, rather than leaving
an old timestamp on a new, smaller backlog.

Authorship currently publishes changed rows in bounded writer turns before its
completion checkpoint. A query may therefore see mixed previous/next output.
The eventual receipt needs to represent this explicitly, or the publication
mechanism needs coherent generation reads. Merely attaching the final completion
checkpoint to an arbitrary query response would make a false promise.

For progress, show done/total and a percentage only when total is known. Unknown
totals get a count when available and an indeterminate bar. Unknown completion
estimates must remain conditional; a scheduled eligibility time is not a promise
of start or finish while gates and shared resources can still block it.

## Required production checks

- Switching or unmounting results updates the header's domain union.
- Label-dependent query controls add/remove their true dependencies.
- Cached/old results retain their consumed revisions until replaced.
- Status disconnect/reconnect and out-of-order snapshots cannot report false
  freshness or clear a current backlog.
- No-known-staleness retains the source observation gap.
- New input during a run survives completion of the older run.
- Idempotent requests, partial progress, retries and shared resource contention
  behave the same in the list, graph, header and result indicators.
