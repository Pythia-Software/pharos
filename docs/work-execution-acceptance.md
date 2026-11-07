# Work execution implementation and acceptance

The design is [work-execution-plan.md](work-execution-plan.md). This record
separates implemented contracts and observed evidence from release acceptance.
No production catalog is used by the experiments. The catalog floor stays 23.

## Stack

1. Contract, classified kinds, isolated workload harness and retained-binary
   storage/recovery oracle.
2. Common in-memory executor, allocation and demand semantics, real stored
   activity/writing/labels/query-fact adapters, resource collection.
3. Remaining domain routing, population pipelines and replacement of ordinary
   backlog launchers.
4. Resource/inspection UI, integrated verification and cutover selection.

The normal executor remains classic until the integrated gates pass. Revert
stack suffixes in reverse dependency order. The plan's comparison-path cleanup
is due no later than the next normal release after acceptance.

## Baseline artifacts

The retained service binary is built from
`cb2ee633ff2c2afd789dd0cd981c8033731bea77`. Its private manifest records its
SHA-256 and Go toolchain. The implementation branch subsequently merged master
through `8978d576`; comparisons pin algorithms, models and workload descriptors.

The completed November 1 backup was cloned into a new experiment master on
Euclid. It contains 53,328 workspaces, 47,056 conversations and 7,407,353
messages (110,637,600,768-byte main catalog, with committed backup WAL).
The master has schema 21; opening an arm includes compatible initialization to
23. The source main/WAL inode, size and modification stamps did not change
through preparation. No SQL connection was opened on the backup or production
catalog by preparation. Private paths/descriptors/reports remain gitignored.

`TestWorkPreviousBinaryRecovery` passed against the actual retained binary:
interrupt after a bounded producer pass, retain the historical fork's dirty
mark, close, let the previous binary recover the current catalog, reopen and
process a subsequent edit. Canonical message count is preserved. This is a
small deterministic fake-model storage oracle, not model throughput evidence.
The selected adapters must satisfy the same invalidation/recovery contract.

## Reproducing isolated experiments

```
python3 tools/work-experiment.py prepare \
  --completed-backup /path/to/completed-backup --master /path/to/new-master
python3 tools/work-experiment.py run \
  --binary /path/to/retained-pharos --master /path/to/new-master \
  --destination /path/to/fresh-arm --descriptor /path/to/private-workload.json \
  --engine classic
python3 tools/work-experiment.py compare /path/to/classic/report.json /path/to/scheduled/report.json
```

Each run clones fresh mutable files. Preparation includes the WAL and refuses
symlinks, overlapping paths, the installed Euclid library, existing destination
artifacts, and a backup that changes while copied. It creates a fresh config
without copied host/source declarations, tokens, cloud or app settings.
The CLI harness allows bounded local analysis commands and no asset fetching
or external imports. Capture/ingestion experiments need the explicit isolated
source replay/probe path; they are not admitted through arbitrary CLI commands.
No hardlinks are used. Clone/prepare time is outside execution timing.

Descriptors name commands, pinned model settings, timezone, timeout,
`semantic_checks` (deterministically ordered SELECT queries returning semantic
columns, excluding volatile timestamps), and `pending_checks` (SELECT queries
whose result must be empty). Queries run read-only on the arm. Only row counts
and streaming digests are exported. An error/timeout or pending required work
fails the arm. Whole-process child CPU is labelled as such; unavailable
simultaneous memory/I/O/model-load data cannot be silently treated as zero.

The probe tier runs real domain methods with service schedules disabled and
`evidenceOnly` set, and queries the actual Query Table product endpoint:

```
go test -c -tags probe -o /path/to/archive.test ./internal/archive
PHAROS_WORK_REPLICA=/path/to/fresh-arm \
PHAROS_WORK_DESCRIPTOR=/path/to/private-readiness.json \
  /path/to/archive.test -test.run '^TestWorkReplica$' -test.timeout 24h -test.v
```

The arm marker is mandatory. The descriptor pins `name`, RFC3339 `clock`,
`stages` and `product`: route `/api/query/message_labels/query-v3`, the actual
version-3 wire query, `expected_sha256`, absolute `latency_ms`, and
`consecutive` observations. Pin the expected result from a fully settled
classic reference before measuring the candidate. The primary surface is the
recent 48-hour message-label table with its stored/live fallback, estimate-
compatible writing quality and current configured label model. The initial
absolute latency criterion is 2,500 ms for three successive observations;
compare both arms with the same criterion. Correct rows can precede query-fact
settlement. This measures the real route; projection emptiness is not readiness.

The report records each query's status, result digest, elapsed/latency and
separate `t_correct`, `t_usable`, `t_settled`. A missing pinned oracle produces
no correctness/usable milestone. Post-settlement observations are outside
execution totals. Query replay cost during execution is included equally.
No transcript bodies are exported. Baseline proof binaries must be compiled
from the retained source plus the unchanged probe, not candidate algorithms.

## Required integrated evidence

Before production acceptance: classic A/A; representative paired pipelines;
full-corpus recapture/reindex and derived-settlement pairs at equal useful
capacity; actual recent arrival during coarse history; semantic parity;
stop/crash/disable/re-enable/cancellation/sharing/continuous-edit tests;
stable model and context loads; bounded inventory/telemetry overhead; actual
old-binary recovery after candidate publication and subsequent old-writer edits.

A reproducible duration or resource ratio >= 1.20 rejects the candidate.
Missing memory/I/O/readiness/rollback evidence leaves the gate incomplete.
A CLI-only comparison never claims production acceptance. Reverse the pair
order when cache/thermal noise or a ratio near the limit makes it ambiguous.

## Scope and quality

The checked [task inventory](work-task-inventory.json) classifies every
registered kind; a new kind without a boundary fails a test. Domain code owns
invalidation/publication. Exact authorship, global identities/grouping/ranking
retain their relational boundary. Labels accept usable stored writing spans;
they do not universally require exact authorship. Query facts can improve
latency while their live fallback already provides correct product rows.
Stored timing is local to a conversation; global aggregates have wider inputs.

The existing AST launch test remains the check for unregistered goroutines.
Fix grouping/title generation is currently inside events/findings and must be
accounted under those admitted coarse phases, not an invisible new worker.
Interactive Thinking fits keep their existing admission; the background CPU
control does not throttle interactive queries or claim Mac-wide enforcement.
