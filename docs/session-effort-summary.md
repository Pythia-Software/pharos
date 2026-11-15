# Observed request effort and summary backfill

The [effort contract](token-accounting.md#agent-effort-levels) separates the
strict existing session configuration from the observed request-effort set.
Unknown startup metadata no longer hides consistently observed request effort
from consumers of work detail, MCP work detail, and delegation. Token and cost
allocation continues to use the strict accounting policy.

## Backfill

After installing this build, use the existing configured catalog command:

```sh
pharos --config /path/to/config.json repair-usage-attribution --session-effort-only
```

This mode reads only session IDs and request effort, writes only the derived
summary table (and cache invalidation metadata), and reports
`agent_sessions_summarized`. It does not replay source adapters, rewrite session
configuration, reconstruct requests, or change usage buckets, tokens or costs.
The projection does not distinguish reset provenance already lost from request
rows, and cannot establish effort for unrecorded requests.

Each transaction handles at most 500 agent sessions and commits its summary
version with those rows. Cancellation/restart retains committed batches; rerun
the same command to finish. A completed repeat reports zero until request
evidence changes. A cache generation change makes API consumers see newly
backfilled summaries. Existing readers can continue while the bounded writes
run; source changes invalidate the summaries of both owners of a moved request.

Unqualified `repair-usage-attribution` retains its existing usage repair and
then finishes any pending effort summaries, including workspaces already at the
current usage-attribution version. Use the qualified mode above when the intent
is only summary repair. Other repair commands retain their existing scope.

Coordination with the accounting repair assignments: summary projection version
`1` is independent of `usageAttributionVersion` (`8` at implementation time).
No accounting or source extractor version bump is needed for this change.
Accounting fixes can bump their versions independently; their request writes
invalidate summaries, and the ledger writer refreshes them in the same
transaction. Do not replace `sessionMessageEffort` with this projection when
assigning aggregate/remainder usage. No Query Table package or bundle patches
are involved.

To inspect saved summaries directly:

```sql
SELECT a.id, a.provider, a.effort AS strict_effort, es.summary_json
FROM agent_sessions a
LEFT JOIN agent_session_effort_summaries es
  ON es.agent_session_id = a.id AND es.version = '1';
```

## Read-only census, November 10, 2026

A SQLite `mode=ro` transaction on Pharos On Euclid streamed only
`(agent_session_id, effort)` from 1,463,801 request rows and joined the 59,370-row
session inventory in memory. It reproduced the original audit in 2.32 seconds.
These are stored session rows, including source/mirror records; they are not
deduplicated executions. The new-summary counts below were verified by the
actual repair on a disposable extraction of those records. The live catalog
was read only throughout.

| Provider | Blank strict effort, every recorded request at one known level | New summary: single level | Of single: includes unknown requests | Multiple levels | Only unknown requests | No recorded requests |
|---|---:|---:|---:|---:|---:|---:|
| Codex | 9,348 | 9,495 | 78 | 9 | 405 | 6,275 |
| Claude | 6,280 | 11,186 | 0 | 10 | 12,592 | 5,760 |

The 15,628-row blank-strict cohort becomes discoverable as a single observed
level with zero unknown **recorded** requests; strict effort remains blank.
The 78 Codex single-level sessions with unknown requests remain explicitly
qualified. Multiple-level and all-unknown sessions retain their distinct states.
Historical coverage remains unproven for every cohort.

## Validation

The disposable extraction retained all columns of 59,370 session rows, 88,620
hourly-usage rows, and 1,463,801 request rows. The production repair populated
59,370 summaries in 37.19 seconds (excluding extraction, index creation and
verification); a second run repaired zero. SHA-256 digests of every row of all
three original tables, ordered by their identities, matched before and after.
The saved summary cohorts matched the census above. This validates the full
recorded population without replaying or copying transcript bodies. Timing is
one observation on this machine, not a performance guarantee.

`TestSessionEffortSummarizesOnlyRecordedRequests` ingests Codex fixtures for
startup metadata, missing request effort, null/empty resets, multiple levels,
multiple levels with unknown requests, wholly unknown effort, the known literal
`none`, and no requests. It checks both work-detail APIs.

`TestSessionEffortBackfillPreservesUsageAndResumes` uses a disposable catalog
whose usage version is already current. It interrupts after a committed batch,
closes/reopens the catalog, resumes, and repeats. Before/after snapshots compare
every session row/ID, every usage bucket and category, each existing bucket's
effort, all requests and messages, workspace metrics, usage repair stamps, and
calculated usage/cost rows. It also checks reset, move, and delete invalidation.

`TestObservedEffortDoesNotAllocateUnknownAggregate` proves that one observed
high-effort request (120 tokens) leaves a 240-token aggregate remainder unknown.
The delegation test checks own-session summaries, unknown embedded children,
and cache refresh. Existing mixed-aggregate and TL1 transcript-change tests
continue to cover their separate accounting and parser policies.

```sh
go test -vet=off -run 'TestSessionEffort|TestObservedEffort|TestEffort|TestDelegationEffortUsesOwnSessionAndConversation' ./internal/archive
GOMAXPROCS=2 PHAROS_TEST_PROCESSES=4 go test -short -timeout 30m ./...
```

Both checks passed. The final fast-tier run used fewer workers after a default
run hit its ten-minute deadline amid concurrent test jobs on this Mac. The
timing-sensitive Ask case from that run also passed when rerun independently.
