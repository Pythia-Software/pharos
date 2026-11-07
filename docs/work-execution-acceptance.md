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
SHA-256 and Go toolchain. That hash is the baseline's engine proof: the
retained binary predates engine selection and always runs classic. The
implementation branch subsequently merged master through `8978d576`;
comparisons pin algorithms, models and workload descriptors.

The completed November 1 backup was cloned into an experiment master on
Euclid with the first harness. It contains 53,328 workspaces, 47,056
conversations and 7,407,353 messages (110,637,600,768-byte main catalog, with
a backup WAL). The master has schema 21. The source main/WAL inode, size and
modification stamps did not change through preparation, and no SQL connection
was opened on the backup or production catalog. That harness checked neither
the backup's completion evidence nor its captures/preserved trees, so this
master does not meet the current preparation contract. It must be prepared
again before it counts as evidence. Private paths/descriptors/reports remain
gitignored.

`TestWorkPreviousBinaryRecovery` passed against the actual retained binary
with the strengthened oracle below. This prefix also passed the fast and
full Go suites, the web checks and full JS matrix, and all eight isolated
experiment harness tests. It is a small deterministic fake-model
storage oracle, not model throughput evidence:

- interrupt a bounded producer pass after a parent edit, so the fork stays
  enrolled, and close;
- run the retained binary's `labels run --json` with a private config, HOME
  and temporary directory, passing through only the fake worker's settings,
  and require its own report to be complete, with the enrolled conversation
  rebuilt and nothing failed;
- require labels for the fork's message and the parent's new message, then a
  forced full classic rebuild that relabels nothing and leaves every stored
  label unchanged (`input_hash` is the framing hash, never compared with a
  message's `content_hash`);
- edit a message, and require the new process to relabel it.

The selected adapters must satisfy the same invalidation/recovery contract.
The old binary still reads host identity through absolute tool paths (such as
`/usr/sbin/ioreg`); the restricted environment does not stop those reads.

## Reproducing isolated experiments

```
python3 tools/work-experiment.py prepare \
  --completed-backup /path/to/completed-backup --master /path/to/new-master
python3 tools/work-experiment.py freeze --master /path/to/new-master \
  --name claude --path /path/to/source-or-model
python3 tools/work-experiment.py run --role baseline \
  --binary /path/to/retained-pharos --retained-sha256 RETAINED_SHA256 \
  --master /path/to/new-master --destination /path/to/fresh-arm \
  --descriptor /path/to/private-workload.json
python3 tools/work-experiment.py compare --mode ab --retained-sha256 RETAINED_SHA256 \
  /path/to/baseline/report.json /path/to/candidate/report.json
```

Preparation needs evidence that the backup completed. With the product's
`pharos-backup.json`, it must record a complete version-1 backup, and the main
catalog's size and modification time must match the snapshot it records. A
WAL beside that snapshot was written later; it is copied and recorded as not
covered by the manifest. A backup without a manifest needs `--attestation`, a
private record with `completed_at`, `method` and `attested_by`. The guarantee
is then only the operator's statement plus the check that nothing changed
while copied. Neither form proves the backup's source was offline, and no
path name is treated as proof.

Preparation stat-walks the catalog, WAL, captures, preserved trees and the
evidence file before and after copying. It refuses any change, including a
change to the evidence bytes. It also refuses symlinks, overlapping paths, the
installed Euclid library and existing destination artifacts. The master
records a stamp of its own files, and `freeze` and every arm refuse a master
that changed after preparation. Nothing is hardlinked. Clone/prepare time is
outside execution timing.

`freeze` copies a source folder or model input into the master once. It
refuses an input that changes while copied and records the copy's content
hash. Descriptors name frozen inputs (`{"input": "kestrel"}` for path
settings, `"input"` for sources), never a host path. Every arm clones from the
master and never rereads a configured live folder. The corpus marker, and so
every input's content identity, must match across arms. Content is hashed
once, at freeze time, never on each run.

Each arm writes a fresh config without host/source declarations, tokens,
cloud or app settings. Commands run with an allowlisted environment: PATH,
the arm's own HOME and TMPDIR, the descriptor's timezone, and an engine
request. Logs, configs and reports are created 0600 inside the 0700 arm.
The CLI harness allows bounded local analysis commands and no asset fetching
or external imports. Capture/ingestion experiments need the explicit isolated
source replay/probe path; they are not admitted through arbitrary CLI commands.

Descriptors are validated before any work. They need a `name`; commands;
pinned model settings; timezone; timeout; a `startup_command` and the
`expected_schema_version` it must reach; non-empty `semantic_checks`
(deterministically ordered queries returning semantic columns, excluding
volatile timestamps); and non-empty `pending_checks` (queries whose result must
be empty). Checks run on the arm through a read-only connection whose SQLite
authorizer admits only reads, so ATTACH, PRAGMA and writes fail. Only row
counts and streaming digests are exported.

The startup command, which carries any initialization or migration (the
November master is schema 21), is timed and reported apart from the work
totals. An arm whose startup does not reach the pinned schema fails. Every
work command is gated on its own ratio, as are the totals, so a shared fixed
cost cannot dilute one step's regression. Durations under five seconds are
reported but ungated. Normalizing a master once with the retained binary
would remove the migration from every arm. That needs a `normalize` command
that clones the master, runs the retained binary's startup on the clone and
marks the result as a new master; it is not implemented.

An error, timeout, failed check or pending work leaves the arm's report with
`settled: false` and the failure, and `compare` refuses an unsettled arm.
Whole-process child CPU is labelled as such. Unavailable simultaneous
memory/I/O/model-load data is never treated as zero.

`compare --mode ab` requires the baseline report's binary hash to equal the
retained artifact hash given to `compare`. The candidate must be a different
binary that reports its executed engine from the path it actually ran. An
environment variable is only a request and never counts. The current CLI does
not report its executed engine, so a candidate arm records it as unavailable,
and an A/B comparison that would otherwise pass returns `refused`. A
regression or semantic mismatch still rejects. `compare --mode aa` takes two
runs of the retained binary and reports noise against the same gates; it makes
no release claim.

The probe tier runs real domain methods with service schedules disabled and
`evidenceOnly` set, and queries the actual Query Table product endpoint:

```
go test -c -tags probe -o /path/to/archive.test ./internal/archive
PHAROS_WORK_REPLICA=/path/to/fresh-arm \
PHAROS_WORK_DESCRIPTOR=/path/to/private-readiness.json \
  /path/to/archive.test -test.run '^TestWorkReplica$' -test.timeout 24h -test.v
```

The arm marker is mandatory. The descriptor pins `name`, RFC3339 `clock`,
`stages`, `pending_checks` (at least one read-only SELECT/WITH statement per
stage) and `product`: route `/api/query/message_labels/query-v3`, the actual
version-3 wire query, `expected_sha256`, `min_expected_rows`, absolute
`latency_ms`, and `consecutive` observations. Pin the expected result from a
fully settled classic reference before measuring the candidate. An empty
result can be correct elsewhere, but the primary surface, the busy recent
48-hour message-label table, pins at least one row. That surface uses its
stored/live fallback, estimate-compatible writing quality and the current
configured label model. The initial absolute latency criterion is 2,500 ms
for three successive observations; compare both arms with the same criterion.
Correct rows can precede query-fact settlement. This measures the real route;
projection emptiness is not readiness.

A product observation counts only when the response is 200 and decodes as a
version-3 result with a rows array of non-null objects. It also needs at least
the pinned row count. Any other response is recorded as invalid with its
reason. The polling goroutine records errors rather than failing the test
from another goroutine. After each stage returns, its pending checks must be
empty before the run continues; `t_settled` is taken only after every stage's
checks pass. Catalog opening and migration are reported as `startup`, outside
the execution totals and milestones. The probe calls the classic domain
methods directly, so its reported engine is always classic. A failed stage
still writes the private report with `settled: false`.

The report records each query's status, row count, result digest or invalid
reason, elapsed/latency, and separate `t_correct`, `t_usable`, `t_settled`. A
missing pinned oracle produces no correctness/usable milestone.
Post-settlement observations are outside execution totals. Query replay cost
during execution is included equally. No transcript bodies are exported.
Baseline proof binaries must be compiled from the retained source plus the
unchanged probe, not candidate algorithms.

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

The checked [task inventory](work-task-inventory.json) maps every registered
kind to an execution class, and each class has one scope, boundary, inputs and
accepted-quality contract. A new kind without a class fails a test, and so does
a floor that differs from the catalog schema version. Domain code owns
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
