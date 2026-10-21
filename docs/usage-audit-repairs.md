# Usage audit repairs

Verified 2026-10-21. The [current audit](usage-audit-current.md) covers 432 cases
across all 17 pages: **351 Green, 81 Yellow, zero Red or Black**, with zero
execution errors, URL mismatches, or broken filter/row-action transitions.
All 359 measured outgoing executions also finished within four seconds.
Sixteen additional live in-app transitions preserved the complete query and
assumptions; all completed in at most 0.36 seconds.
The [baseline](usage-audit.md) retains its original 235 Green, 179 Yellow,
15 Red, three Black, and 16 error cases. Private checkpoints remain under
`.context/usage-audit/`.

| Checklist | Change | Validation |
| --- | --- | --- |
| Carbon assumptions survive shared URLs | `usage_carbon` records the resolved calculation and comparison choices; Usage rows, metrics, and grouped queries receive request-scoped assumptions | Focused browser replay passes; calculation and request-isolation tests |
| Legends and expanded series survive URLs | Chart state includes selections and series limit; older URLs merge with current defaults | All five targeted control cases pass fresh-context replay |
| Query to dashboard retains edited filters | Return links restore source WHERE; incompatible populations and discarded HAVING predicates have explicit reset labels | Model tests and focused browser checks |
| Legacy analysis transitions preserve context | Compatible links carry their query and chart settings; incompatible analyses disclose resets | Model tests and focused link checks |
| Library search transition discloses scope | Link says “Search all conversation text” and explains that query filters do not follow | Focused link checks |
| Tool summary / Skill actions work with hidden columns | Record responses include the small source identity fields required by their actions; grouped records are excluded | Default, minimal-column, and paged real-data actions pass |
| Large grouped queries avoid staging raw populations | Eligible integer counts/sums/means reduce in SQLite before the adapter stages the compact groups; adapter still owns identity, HAVING, paging, and result metrics | Complete-response parity matrix against the general executor; all sampled groups and links pass |
| Broad keyword queries complete within the existing budget | Exact substring indexes use the compiler’s Unicode lowercase function followed by case-sensitive trigrams; complete indexes can answer supported positive/negative predicates without rereading every payload | Backfill, update/delete, case/null/empty, and typed-filter parity tests; all sampled keyword combinations pass |
| Tool summary paging and metric groups | Call-count paging has a typed numeric index; compact summary metrics use the same 10,000-group budget as V3 | Focused rerun passes |
| Distinct-value lookups finish reliably | Tool dimensions and derived operation identities use the equivalent daily summary population; other lookups prune unused joins | Distinct-value parity tests and no observed metadata failures in the final matrix |
| Metadata does not delay data requests | The host shares repeated suggestion requests and serializes background metadata on the network, leaving browser connections for rows and groups | Request-sharing, expiry, retry, and subscriber-cancellation tests; all outgoing browser checks finish within four seconds |
| Missing-date Calls links keep full identities | All 13 summary dimensions follow the row action; a stored-calendar index avoids scanning chronological records to their end | Exact summary count compared with destination count; null/invalid-date and executor parity tests |
| Empty searches and past-end pages finish promptly | Once the exact total proves the page empty, the adapter builds its normal empty response without a chronological scan | Empty/past-end page parity; empty raw-call keyword query now 0.38 seconds |

The first focused repair run, `repair-correctness-01`, completed **13/13 Green**,
with **zero errors, URL mismatches, or link failures**. This verifies the targeted
correctness repairs; it is not the final 432-case performance result.

The text indexes backfill in bounded transactions and maintain new changes with
triggers in the source write transaction. Until a backfill is complete, requests
keep the ordinary exact predicate. Readiness includes both indexes, so a dev
service can wait for them before benchmarking. Short or non-ASCII search needles
and other text operators retain the general predicate. The existing 2.5-second
interactive search budget remains unchanged. A narrow per-call timing projection
shares raw row IDs with the text index, allowing exact membership lookups to read
activity, categories, providers, dates, and durations without touching payload
pages. Its build version follows the tool-rollup classification version; an
upgrade backfills before activating the new projection.

The projection also stores the local calendar day. Its version includes the
classification and timezone identity; old scalar facts remain usable while a
calendar upgrade completes. Calendar upgrades read the compact facts instead
of wide source payloads, and completed cursors are removed so returning to a
previous timezone cannot skip a rebuild. New, changed, and deleted source calls
maintain facts and text membership atomically.

For supported broad command exclusions, the executor starts with the fresh
cube's nonempty-command population and subtracts the union of positive text
matches in the same read snapshot. Counts, sums, means, nulls, and group identity
retain their ordinary meaning. Nested ORs and unsupported shapes fall back to
the general path. This reduces the work without extending request deadlines.

No Query Table dependencies or library files have been modified. The new
accelerators are host integration paths alongside the existing native metrics
and indexed paging. Reusable opportunities are described in the self-contained
[upstream handoff](usage-query-upstream-handoff.md).

Metadata suggestions share in-flight reads and retain their result object for
five seconds. This cache affects suggestions and field statistics only. Row,
metric, and grouped-query results retain their normal snapshot semantics.

The first full repair sweep (`repair-full-02`) resolved every URL mismatch and
left five slow Agent time cases. The following focused sweep showed that broad
command membership still caused wide-record reads. Those results are retained
as checkpoints and are superseded only when the same cases complete in a later
selected run. No timeout, missing replay, or unexecuted transition is counted as
a successful check.

The development launcher now finishes tool-summary work inherited from a copied
in-progress sync before starting benchmarks. Text/projection preparation adds a
one-time backfill and maintains its derived records in each source transaction.
Production source captures and saved user preferences were not modified.

Final evidence selects `repair-full-03` and `repair-final-affected-01`, with the
later result replacing the same case. The latter reruns every Agent Time, raw
Tool calls, and Carbon case after the last scan/empty-page fixes. It also fixes
an audit gap: API-only Carbon transitions now forward the URL's assumptions,
and the execution cache distinguishes those contexts. Interrupted diagnostics
and profiling probes are excluded. Each page retains at least 25 distinct
parameter combinations and every selected case completed a fresh-context replay.

The remaining 81 Yellow primary views finish below four seconds; the largest
observed primary load is 3.73 seconds. Further Green improvements can build on
the upstream reducer/metric-fusion handoff without changing query semantics or
raising deadlines. These measurements sample the combination space on this
development snapshot; they are not a guarantee for every query or machine.

Validation: fast and full Go suites; 569 JS tests in both regular and full modes;
types, schemas, recipe and rebuilt-bundle checks; 63 selected browser checks;
13 Python tool tests; audit-script syntax and whitespace checks. See the current
audit for commands and execution provenance.
