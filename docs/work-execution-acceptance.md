# Work execution implementation and acceptance

The design is [work-execution-plan.md](work-execution-plan.md). This record
separates implemented contracts and observed evidence from release acceptance.
No production catalog is used by the experiments. The catalog floor is 24
after the main integration; the executor adds no further compatibility-floor
change.

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

The rebased stack pins main commit
`108c0367bab0b94466fb0f1c8114029e8d4d4a3d` (schema 24, without the
executor), recorded in the [task inventory](work-task-inventory.json).
Main now uses the native fingerprint renderer and shared file locks. A retained
service and both probes must be rebuilt from this baseline and the same common
probe bytes, with hashes recorded privately, before paired measurements count
as evidence. Prior binary proofs and throughput observations are historical.

The preceding main baseline was `289ad3aa1c1f40dfc0c215dcf77c61eff0241beb`;
its retained service SHA-256 was
`aad36b01c062df31f10611b60def6b6145557238a1c8187baaa2cb3cf7b73f5e`.

Historical, not acceptance for this baseline: the earlier retained binary was
built from `cb2ee633ff2c2afd789dd0cd981c8033731bea77` (schema 23).
`TestWorkPreviousBinaryRecovery` passed against it with the oracle below, and
earlier diagnostic and preparation runs used it. Those results predate the
main integration and must be repeated with the schema-24 baseline.

`TestWorkPreviousBinaryRecovery` passed against the retained schema-24
binary during this review cycle. The fast Go suite, the two probe fixture
tests and 26 harness fixture tests also passed. The recovery test is a small
deterministic fake-model storage oracle, not model throughput evidence:

- interrupt a bounded producer pass after a parent edit, so the fork stays
  enrolled, and close;
- run the retained binary's `labels run --json` with a private config, HOME
  and temporary directory, passing through only the fake worker's settings,
  and require its own report to be complete, with the enrolled conversation
  rebuilt and nothing failed. Only the fields the oracle reads are decoded,
  by name, and each must be present; a later change to `LabelsRunStats`
  cannot fail a proof about a fixed binary;
- require labels for the fork's message and the parent's new message, then a
  forced full classic rebuild that relabels nothing and leaves every stored
  label unchanged (`input_hash` is the framing hash, never compared with a
  message's `content_hash`);
- edit a message, and require the new process to relabel it.

The selected adapters must satisfy the same invalidation/recovery contract.
The old binary still reads host identity through absolute tool paths (such as
`/usr/sbin/ioreg`); the restricted environment does not stop those reads.

### Corpus

The November 1 backup on Euclid holds 53,328 workspaces, 47,056
conversations and 7,407,353 messages (a 110,637,600,768-byte main catalog at
schema 21 with a 64 MiB WAL). It has no `pharos-backup.json`, and nobody has
attested its completion. The corpus has never been labeled. The first
experiment master made from it (version 1) and its arms stay diagnostic.
Version-3 evidence starts from `import-snapshot` below, which records that
derivation honestly instead of claiming a completed backup.

## Experiment masters

Every master is a directory with `catalog`, `captures`, `preserved` and
`inputs`, plus a marker that records a stat stamp of those files, the corpus
counts, the schema, the frozen inputs and a lineage of the steps that made it.
Every command refuses a master whose files changed after its marker was
written, and refuses version 1/2 artifacts.

- `prepare` copies a completed backup. With the product's
  `pharos-backup.json`, it must record a complete version-1 backup, and the
  main catalog's size and modification time must match the snapshot it
  records. A WAL beside that snapshot was written later; it is copied and
  recorded as not covered by the manifest. A backup without a manifest needs
  `--attestation`, a private record with `completed_at`, `method` and
  `attested_by`; the guarantee is then only the operator's statement. Only
  the catalog, captures and preserved trees are copied. The evidence's
  `excluded_items` names everything else the backup holds (config, library
  identity, `hosts/` source declarations): arms write their own config and
  replay no source.
- `import-snapshot` admits an existing experiment master. It stat-walks the
  source's catalog, WAL, captures, preserved tree, inputs and marker before
  and after an APFS clone and refuses any change. It then opens only the
  clone: recovers the WAL with a truncating checkpoint, runs SQLite's
  `quick_check` (or `integrity_check`), checks the schema window and counts
  the corpus, which must equal the counts the source recorded. The evidence
  basis is `derived-experiment-snapshot` with `completion` and `offline`
  both `unattested`. `--observe-original` adds a stat-only comparison with the
  backup the source came from: catalog/WAL/-shm sizes, modification times and
  inodes, and capture and preserved file names and sizes. It never opens SQL
  on or reads content from that backup.
- `normalize` clones a master and runs the retained binary's startup command
  once on the clone (default `labels status`), with a private config and
  environment. The result must reach the pinned schema (the inventory's
  `catalog_compatibility_floor`, 24), keep the same corpus counts and pass
  the integrity check. Every later arm then starts at that schema, so no
  arm's startup includes the migration.
- `promote` freezes a settled arm of the retained-source classic probe as a
  new master. The representative workload uses it to drain every non-label
  queue once, so a classic arm's global queues and a scheduled arm's
  population cover the same work.

None of these is evidence that the original backup completed or that its
source was offline. They prove that the experiment corpus is
SQLite-consistent, stable while copied and the same corpus in every arm.

`freeze` copies a source folder or model input into a master once. It
refuses an input that changes while copied and records the copy's content
hash. Descriptors name frozen inputs (`{"input": "kestrel"}`, or
`{"input": "app", "path": "pharos-labeler-ort"}` for a file inside one),
never a host path. Every arm clones from the master and never rereads a
configured live folder. Content is hashed once, at freeze time. The installed
model directories and app executables are only read when frozen; nothing
writes to them.

No source, input or destination may lie inside, or contain, a library Pharos
uses on this Mac: every library named in an application registry
(`applications/*/registry.json` in the support directory, read as JSON
metadata only), the default library's data in the support directory, new
local libraries under its `Libraries` folder, and the development library
`/Volumes/euclid/Pharos`. An unreadable registry refuses rather than
guessing. Completion still comes from the backup's manifest or an explicit
attestation, never from a directory name. The probe, whose `HOME` is the
arm's own, keeps only the `/Volumes/euclid/Pharos` backstop and requires the
version-3 arm marker the harness writes.

## Reproducing isolated experiments

```
python3 tools/work-experiment.py import-snapshot --source-master OLD \
  --master IMPORTED --observe-original BACKUP
python3 tools/work-experiment.py normalize --master IMPORTED --destination NORMALIZED \
  --binary RETAINED_PHAROS --retained-sha256 RETAINED_SHA256
python3 tools/work-experiment.py freeze --master NORMALIZED --name kestrel-68m-tpool-2 --path MODEL_DIR
python3 tools/work-experiment.py probe-run --role baseline --engine classic \
  --probe RETAINED_PROBE --probe-sha256 RETAINED_PROBE_SHA256 \
  --baseline-probe-sha256 RETAINED_PROBE_SHA256 \
  --master NORMALIZED --destination FRESH_ARM --descriptor WORKLOAD
python3 tools/work-experiment.py promote --arm SETTLE_ARM --master SETTLED \
  --baseline-probe-sha256 RETAINED_PROBE_SHA256
python3 tools/work-experiment.py pin-oracle --reference REFERENCE_ARM/report.json \
  --descriptor WORKLOAD --output PINNED_WORKLOAD
python3 tools/work-experiment.py probe-compare --mode aa|ab \
  --baseline-probe-sha256 RETAINED_PROBE_SHA256 A/report.json B/report.json
```

Clone, prepare, normalize and promote times are outside execution timing.
Run one measured arm at a time.

## Arms

Each arm writes a fresh config without host/source declarations, tokens,
cloud or app settings, with `library = false`. Commands run with an
allowlisted environment: PATH, the arm's own HOME and TMPDIR, the
descriptor's timezone, and an engine request. Logs, configs and reports are
created 0600 inside the 0700 arm. Checks run on the arm through a read-only
connection whose SQLite authorizer admits only reads, so ATTACH, PRAGMA and
writes fail. Only row counts and streaming digests are exported. A digest
is order-independent (the sum of row hashes, basis `multiset-sha256-v1`), so
a check without `ORDER BY` cannot mismatch on row order alone; comparison and
oracle pinning refuse reports with another digest basis.

Both descriptor kinds share one `pending_checks` shape: each domain (a CLI
domain name, a probe kind or a legacy stage) maps to a non-empty list of
single read-only `SELECT`/`WITH` statements whose results must be empty.
Each statement is digested separately as `domain#index`.

A CLI arm (`run`) times a `startup_command` apart from its work commands and
requires the pinned schema. The CLI allows bounded local analysis commands
and no asset fetching or external imports. Every work command is gated on its
own wall and CPU ratios, as are the totals and the startup command. Each
ratio is gated when either arm's quantity is at least five seconds, so a
mostly-waiting step's long wall time does not turn noise in its tiny CPU into
a rejection, while a step that climbs from under the floor (or from zero) to
above it is a regression. The CLI does not report its executed engine, so a CLI candidate's
engine is unavailable and an A/B comparison that would otherwise pass returns
`refused`. A CLI comparison never claims production acceptance.

A probe arm (`probe-run`) runs the compiled probe test binary. The probe file
uses only symbols present in both the retained source and the candidate, and
drives the scheduled engine only through `POST /api/work` and `GET
/api/work`, so it compiles against both and imports no runtime helper. In
this PR no build has that API, so only classic arms can settle; a scheduled
request fails the arm rather than running classic under another name. The
probe:

- restores production durability (catalog durability, the labeling ledger's
  `synchronous=FULL` and drive flushes), which the test harness otherwise
  replaces with disposable-file settings;
- reports catalog opening/migration as `startup`, outside execution totals,
  and records Unix-time boundaries for `startup`, `work` (from the frozen
  population to settlement) and `readiness` (from the start of work to the
  later product milestone), which scope the harness's samples;
- takes the executed engine from the process's own work API. A build without
  the API answers 404 and runs classic; a request the process did not honour
  fails the arm. A scheduled arm submits every phase as a work request,
  waits for all of them to end, and records each request's facts and every
  task's batches, committed units, work and select time. A backfill window
  that the build does not echo back fails, rather than silently demanding the
  whole corpus;
- freezes each phase's activity population (conversations with a message in
  the window) and records its count and digest. Each kind runs over the union
  of the phases that demand it, recorded per kind, in both arms. Classic
  drains each kind's production whole queue, so a phase without a window
  must demand it. Both harness and probe refuse window-only kinds, including
  labels: restricted labels runs change reuse and invalidation behavior;
- runs the classic kinds in dependency order: timing, writing estimates,
  labels, label query facts. A classic loop ends when its queue is empty; a
  read error, or a writing-estimate queue that does not shrink for three
  rounds, fails the stage instead of spinning to the timeout;
- requires every kind's `pending_checks` to be empty after it settles, and
  the harness checks them again independently, with the semantic digests,
  after the probe exits.

The workload pins `name`, RFC3339 `clock`, `phases` (or legacy classic-only
`stages`), `pending_checks`, `semantic_checks`, model settings and `product`:
route `/api/query/message_labels/query-v3`, the actual version-3 wire query,
`expected_sha256`, `min_expected_rows`, absolute `latency_ms`, and
`consecutive` observations. A product observation counts only when the
response is 200 and decodes as a version-3 result with a rows array of
non-null objects and at least the pinned row count. `t_correct` is the first
observation equal to the pinned oracle; `t_usable` ends the first run of
`consecutive` correct observations within the latency limit; `t_settled` is
taken after every kind's checks pass. A missing oracle produces neither
milestone. `pin-oracle` writes the pinned workload from a settled
retained-source classic reference; the primary recent surface pins at least
one row. The initial absolute latency criterion is 2,500 ms for three
successive observations. Post-settlement observations are outside execution
totals.

### Resource accounting

The harness runs each measured command in its own process group and samples
the process tree about once a second with `proc_pid_rusage`. Gated probe
figures cover the execution window only: startup (gated apart) and the
post-settlement product observations and checks are outside them.

- probe CPU for a window is read by the probe at its boundaries: user +
  system time of the probe and every live descendant (model workers),
  each with its reaped children. The end total less the start total is
  exactly the CPU inside the window. It includes the 1 s product poll during
  work, which both arms run. Where another process cannot be read (off
  macOS) the window reports `cpu_complete: false` and its CPU is a missing
  metric, never a smaller number;
- memory, disk bytes and host noise for a window come from the samples taken
  inside it; a window without one is unavailable;
- the whole probe process's `wait4` CPU, peak footprint and disk bytes
  include startup and the post-settlement checks. They are recorded as
  `whole_process` diagnostics and never gated;
- a CLI command's CPU is its own `wait4` rusage (user + system): the
  command and every descendant it waited for, including model workers. The
  harness observes the exit without reaping it, kills the process group if
  the timeout passes first, then reaps exactly that pid. Nothing else the
  harness runs is counted, including the sampler's own `ps` processes;
- each entry and arm records `cpu_basis` (`wait4-command-v2`). Compare and
  oracle pinning refuse arms whose basis differs or is missing;
- simultaneous memory is the largest sampled sum of the tree's physical
  footprints, so a spike between samples can be missed; each process's
  lifetime peak is reported separately and never summed;
- disk bytes are each process's counters at its last sample, so they are
  lower bounds;
- model loads are the distinct labeler worker processes observed;
- other processes' CPU and the load average are recorded as host noise,
  never subtracted. A pair where either arm's mean other-process CPU exceeds
  one core during its work window, or during its wait for readiness, is
  `refused` until it is rerun quietly or in reverse order. A missing noise
  reading for a window of five seconds or more is unavailable, never quiet,
  and refuses the pair too.

Descendants still alive when the command exits are killed and fail the arm,
because their CPU would otherwise be missing. Unavailable data is reported as
unavailable, never as zero.

### Verdicts

`probe-compare --mode ab` requires the same workload, corpus marker and
population digests, the retained-source probe hash on the classic baseline,
and a candidate whose executed engine was observed through the work API.
Semantic digests and the final product rows must match each other and the
pinned oracle. `t_settled`, `t_correct`, `t_usable`, and the work window's
CPU, simultaneous footprint and disk bytes are each gated at a ratio of 1.20.
A metric is gated when either arm reaches five seconds or 64 MiB; from a
zero baseline, reaching the floor rejects. A candidate that never reaches a
milestone the baseline reached rejects; startup wall time and CPU are gated
separately by the same rule. The outcome is `rejected`, `refused` or
`incomplete`, never accepted: one workload is one gate, and the remaining
evidence is listed. `--mode aa` takes two retained-source runs and applies
the noise rule first: a noisy or unmeasured A/A is `refused`; otherwise it
reports `exceeds-gate`, `incomplete` (a missing metric) or `within-gate`
against the same gates, startup included. It makes no release claim.

## Workloads

- Representative: recent 48-hour demand alongside whole-history demand for
  every measured kind, on the settled-except-labels master. Classic uses its
  production whole queue, while scheduled can prioritize the recent population;
  the product poll measures recent readiness in both. Window-only labels are
  refused: the restricted classic method forces rebuilds and omits durable
  dependent marking, so the earlier two-window workload is diagnostic only.
  On the never-labeled master this is cold whole-corpus labeling and also
  supplies the labels portion of the full-settlement gate below: run and count
  that pair once. A separate short incremental pair needs a master with all
  history labeled and only bounded recent inputs dirty; that preparation and
  paired measurement remain unmet.
- Full settlement: the recent 48 hours plus all history, every kind. Because
  the corpus has never been labeled, this labels every conversation and takes
  hours. It is the release gate for derived settlement and stays explicitly
  unmet until both arms run it.

Actual recent arrival during coarse history needs a source replay into the
arm, which the probe does not do yet; that gate is unmet as well.

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

Every registered kind names its execution class in its `operationKinds` row
(`internal/archive/operation_kinds.go`), the one registry of kinds. The checked
[task inventory](work-task-inventory.json) gives each class one scope,
boundary, inputs and accepted-quality contract. A kind without a known class,
or a class without a complete contract, fails a test. The inventory also
records the retained baseline (`baseline_commit`,
`catalog_compatibility_floor`); the harness reads both and checks corpus
compatibility when admitting an experiment. The floor is retained-artifact
metadata, so an unrelated catalog migration does not fail the default suite.
The plan closes `comparison_window` when it removes the comparison path.
Domain code owns
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

## Review-cycle validation (November 7)

Main `289ad3aa` was merged throughout the stack before resolving the posted
reviews. Independent reviews covered the common harness, executor,
domain routing and resource UI; the resulting fixes were verified on the
integrated stack, with focused checks on independent lower prefixes.

| Check | Result |
| --- | --- |
| `go test -short ./...` and `go test ./...` | Passed (four test processes) |
| Targeted `go test -race` | Passed: sharing, modes, retry, timed policy, label/event sessions, resource history and lease ownership |
| `go vet -tags probe ./internal/archive` | Passed |
| Actual retained-binary proofs | Passed: previous-binary recovery and recovery after scheduled publication, plus the common population/CPU-window fixture tests |
| `npm --prefix web run check` | Passed, including 1,533 unit tests (three skipped) |
| `npm --prefix web run test:full` | Passed, 1,533 tests (three skipped) |
| Selected browser focus/layout checks | 14 passed, including wide/narrow Work and activity-panel states |
| Isolated Python experiment-harness fixtures | 26 passed |

View controls and site-map artifacts were regenerated and reviewed. The
race-enabled cancellation case now keeps interrupted labels pending rather
than charging a failure; failed durable fork marks force rediscovery in
both model domains. Broker and standalone collectors use the slower lease
cache, and cached ownership cannot survive PID reuse. These are correctness
and tooling checks, not realistic throughput acceptance.

## Evidence status

Historical diagnostic runs used the schema-23 baseline. One preparation arm
settled in 489 seconds of work (503 seconds including startup and checks),
but substantial concurrent host load and the earlier accounting make that
unsuitable for acceptance. The subsequent reference arm failed because
labels returned before full settlement. No successful paired result exists.

Nothing in this PR is release acceptance. No version-3 A/A or A/B
comparison against the schema-24 baseline has run. Still unmet:
normalization of the corpus to schema 24 with that binary, representative
and full-settlement pairs,
full-corpus recapture/reindex, actual recent arrival during coarse history
(source replay is not implemented), and quantification/admission of retained
preparation-context memory. Context memory is shared process memory, not an
enforced per-task grant. The classic executor stays the default.

## Review cycle 3 comparison policy

Each probe records the process CPU availability and whether it can enforce the
requested global allowance, with its effective cap taken from the work API.
Classic explicitly reports unsupported. Default-versus-default measurements
compare the intended product policies; an explicitly pinned `cpu_slots` pair
requires both arms to support and enforce the same allowance and is refused
otherwise. Old reports lacking this evidence are refused. Startup arms with
orphaned descendants fail, just like work arms.
