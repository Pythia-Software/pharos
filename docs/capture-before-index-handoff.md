# 2. Preserve evidence before indexing


## Implementation update (2026-10-23)

The shared acquisition boundary and source matrix are implemented in
[`incremental-sync.md`](incremental-sync.md#source-acquisition-rollout). Filesystem
acquisition is experimental and requires `retained_input_capture = true` until
its inventory retention and backup path counts are bounded; default indexing
continues to read live inputs. Opt-in imports preserve before parsing; Codex SQLite naming dependencies have a gated
logical acquisition path. Direct/API/CLI and retained indexing share the
contract. Metadata backfills read existing captures or same-host live inputs
without creating additional captures; offline rebuilds use retained evidence
only. SQLite logical
group acquisition is available for TL1/Antigravity with `sqlite_record_capture`;
Conductor retains its candidate/session implementation. SQLite defaults remain
unchanged pending the documented performance gates. TL1/Antigravity/Codex metadata still need
candidate-level acquisition before default enablement. Logical group verification uses pinned evidence and isolated comparisons. This staged change does not close the all-source rollout
or authorize real-library validation/backfill. The previous completed prerequisite
was committed as `ed47b7c` before starting this implementation.

## Implemented prerequisite API (2026-10-23)

Handoff 1 now provides [SQLite row evidence v1](sqlite-records.md). Use that
specification's actual paths, commands, coverage receipts, budgets and locking
contract; the illustrative names below are not alternative formats. Automatic
SQLite cutover is still gated on hot 11 GB validation. Native/JSONL compatibility
and catalog backups remain enabled. No real historical backfill has run.

Conductor's opt-in `sqlite_record_capture` uses
`acquireConductorSession` and parses a pinned committed revision after releasing
the host capture lock. `reconcileSQLiteLive` and `sqlite-records reconcile`
perform bounded full scans; `sqlite_record_coverage` exposes overdue/completed
coverage. The default interval is 24 hours. TL1/Antigravity/Codex live cutover
remains work for this handoff; their explicit captures already export raw rows.
Do not interpret partial entity captures as full database coverage or require a
whole native Conductor backup each tick.


Status: implementation handoff, not shipped behavior. Written 2026-10-23 after
[cloud backup PR #234](https://github.com/Pythia-Software/pharos/pull/234) was
merged as `98d268a69d8369f2c935b1cd4710940e451354d2`.

Implementation order: **2 of 3**, after
[1. Durable SQLite session captures](sqlite-session-capture-handoff.md) and
before [3. Historical backfill](capture-history-backfill-handoff.md).
Cloud backups are already complete. The source-specific rollout rules are:

1. Filesystem capture-before-index enforcement can land independently.
2. For SQLite sources, land [durable SQLite session captures](sqlite-session-capture-handoff.md)
   and their replay/retention integration **before** enabling this enforcement.
3. Complete enforcement across every production ingestion path, then run
   [historical backfill](capture-history-backfill-handoff.md) as its own milestone.

All handoff numbers refer to this implementation order.
Do not enable a whole-database-per-tick bridge for SQLite or stop Conductor
automatic indexing while waiting for the exporter. A filesystem-only PR must
explicitly leave the existing SQLite preservation gap open until that cutover.
These are separate changes with a shared contract; do not rework the completed
cloud-backup PR.

## Goal and user intent

The user reasonably assumed Pharos always preserved a source before indexing
it. That is currently true of **Capture & index**, but false of several direct
ingestion paths. Fix that sequencing gap.

The desired pipeline is:

```text
discover candidate changes
  -> acquire a consistent input and all parsing dependencies
  -> commit that evidence durably under the library's capture root
  -> parse that exact retained input
  -> commit catalog rows and their evidence identity
  -> update index state and derived views
```

An acquisition can inspect source metadata to discover work. It must not make
new conversation content authoritative in the catalog until the evidence used
to produce that content is durable. Taking a capture and subsequently parsing
the live source does not satisfy this contract.

Cloud backups continue to include both the catalog and retained evidence. The
user explicitly accepted keeping the catalog; source-only backup is not part of
this work. Optional cloud encryption, upload permissions, schedules and the
Beta UI are already implemented and should retain their current behavior.

## What is wrong today

There are two existing pipelines, deliberately distinguished in the current
documentation:

- **Capture & index** copies source evidence, then indexes captures.
- **Index changes** and opt-in automatic indexing read live sources directly.

See [incremental-sync.md](incremental-sync.md), especially its opening sections,
and [configuration.md](configuration.md#capture). Those descriptions are
accurate for the current code; update them when behavior changes.

The distinction was confirmed by a read-only investigation of the real library:
native captures were last refreshed on October 8 and October 15 on its two Macs,
while live indexing continued into October 22–23. Recent indexed conversations
therefore need not have corresponding retained source bytes. Source-level
`coverage: complete` describes indexing coverage; it does not establish durable
preservation of every indexed revision.

SQLite capture has a second, separate limitation: snapshots rotate. The
default `capture_snapshot_generations = 1` retains the latest snapshot plus one
previous generation. A deleted session can eventually disappear from every
retained native snapshot while remaining in the catalog. This handoff fixes
acquisition sequencing; handoffs 1 and 3 cover long-term session retention and
historical recovery. SQLite retention/replay support precedes its sequencing
cutover. Do not claim all three problems are solved by this PR.

## Read these code paths first

All Go implementation files below are in `internal/archive`, one package.

| File or symbol | Why it matters |
| --- | --- |
| [`incremental_sync.go`](../internal/archive/incremental_sync.go): `runIncremental`, `prepareSyncSource` | Both manual and scheduled incremental ingestion; currently constructs live adapters and calls `IngestContext`. |
| [`api.go`](../internal/archive/api.go): direct sync ingestion | Another server path constructing a live adapter and calling `IngestContext`; do not fix only the scheduled loop. |
| [`cli.go`](../internal/archive/cli.go): `ingest` and repair commands | CLI entry points must obey the same acquisition rule. |
| [`ingest.go`](../internal/archive/ingest.go): `Ingest`, `IngestContext`, `IngestExisting`, `guardCapture`, `recordCopyVersions` | Catalog writes, writer attribution, source versions and protection against replaying older copies. |
| [`capture.go`](../internal/archive/capture.go): `Capture`, `captureManifest`, `sourceCapture.commit` | Existing retained files, atomic manifest publication, locking, cancellation, space reserve and durability. |
| [`capture_plan.go`](../internal/archive/capture_plan.go): `planCapture` | Inventories the evidence each source adapter currently consumes. |
| [`capture_sqlite.go`](../internal/archive/capture_sqlite.go) | Consistent SQLite online backup, source/WAL identity, snapshot generations and pruning. |
| [`capture_index.go`](../internal/archive/capture_index.go): `captureView`, `openCaptureAdapter`, `IndexCapture`, `indexGenerations` | Existing off-host capture reader, path remapping and Conductor-only salvage of superseded snapshots. |
| [`capture_index_api.go`](../internal/archive/capture_index_api.go) | Index work plans, progress, source status and captured-host inventory. |
| [`incremental_audit.go`](../internal/archive/incremental_audit.go): `freezeAuditRecord`, `readCapturedAuditRecord`, `auditUnit` | Verification already freezes inputs, but its temporary evidence is removed afterward; it is not durable capture. |
| [`incremental_recovery.go`](../internal/archive/incremental_recovery.go) | Coordinated capture/index, full-source recovery and retained-record repair. |
| [`ingest_plan.go`](../internal/archive/ingest_plan.go), [`ingest_parts.go`](../internal/archive/ingest_parts.go), [`incremental_state.go`](../internal/archive/incremental_state.go) | Discovery plans, per-part checkpoints, extractor versions and incremental skip logic. |
| [`durability.go`](../internal/archive/durability.go), [`backup.go`](../internal/archive/backup.go) | External-drive durability and backup/capture coordination that must continue to work. |

Search current code for all production calls to `IngestContext`, `Ingest`,
`IngestExisting`, and adapter discovery/repair methods. This table is a starting
inventory, not proof that no additional reader exists. Include parser upgrade,
usage repair and enrichment paths when they import new source evidence.

## Scope and entry-point rules

1. Manual **Index changes**, automatic indexing, direct API sync and CLI ingest
   must use retained inputs. Prefer one shared acquisition boundary over a
   separate implementation per caller.
2. **Capture & index**, `pharos capture`, `pharos index`, onboarding/add-this-Mac
   and all-host indexing keep their existing useful separation. Indexing
   previously captured evidence must not demand the original application.
3. Full recovery/repair must acquire evidence before incorporating new live
   data. Pure re-derivation from already retained records can stay offline;
   distinguish it from importing new evidence.
4. Auditing should compare against a pinned retained revision. A temporary
   audit copy must not be reported as successful long-term preservation.
5. Every configured adapter kind needs an explicit production behavior, even
   when it is not eligible for automatic indexing. After a source's cutover,
   unsupported acquisition must be an explicit error, never an unnoticed
   fallback to live parsing. A staged PR must identify sources not yet cut over
   and must not label their existing live-ingest behavior preserved.

Preserve current source opt-ins, host scope, source/account attribution,
selection scopes, retrieval-only capabilities and mothballed reclamation
boundaries. No broader home-directory scan or source mutation is authorized.

## Acquisition and reader contract

Use or extend the existing capture/view machinery. A useful result is a pinned
retained-input handle containing:

- Original host, source name/kind/account, source origin and native group IDs.
- The committed manifest or revision identity and dependency content hashes.
- Reader mappings for the exact retained files/snapshots, not live paths.
- Source observation versions, acquisition completeness and dependencies that
  were missing, unreadable or changed during acquisition.
- A lifetime/lease that prevents replacement or pruning while parsing,
  verification or publication still depends on the referenced evidence.

Names and API shapes are illustrative. Agree on the boundary with handoff 1;
its durable session captures must supply this guarantee before SQLite cutover.
Do not impose a global architecture rewrite just to introduce this boundary.

For filesystem transcripts, keep the current stable-copy, append/rewrite and
missing-file retention semantics. A group includes its dependencies: Claude
subagents and indexes; Codex naming metadata where used; Antigravity summaries,
annotations, selected full transcript, relevant CLI logs and request data;
TL1 registry mappings and supporting transcript/log evidence. A captured root
JSONL without metadata that influenced the parser is insufficient.

For SQLite automatic/manual incremental indexing, use handoff 1's transactional
retained-record exporter. Existing explicit native snapshots may still use the
read-only online backup API. Never copy live database, WAL and SHM independently.
Record enough identity to show what was read; filesystem mtimes remain the
catalog's copy-version order but are neither transaction identifiers nor proof
that content is unchanged. Follow handoff 1's origin and version invariants.

Discovery plans created against live files may be stale after acquisition.
Rebuild or bind/revalidate them against the pinned retained inventory before
using them to skip parsing or report counts. Do not attach a live plan to a
different captured revision merely because the source path matches.

## Commit, failure and concurrency semantics

The required ordering is **durable evidence first, catalog commit second**.
There is no atomic transaction spanning SQLite and the filesystem. Design for
the safe asymmetry: a crash may leave unused evidence; it must not leave newly
indexed data whose only evidence was an uncommitted temporary file.

- Flush data, validate its identity and publish its manifest durably using the
  repository's existing macOS/device-flush mechanisms. Rename alone is not
  enough to promise survival of unplugging an external drive.
- A failed or cancelled acquisition must not advance that input's index
  checkpoint, parser digest, successful-source state or preservation receipt.
- `errCaptureBusy` is coordination deferral, not an acquisition failure. The
  non-blocking per-host flock can collide with manual capture or Capture &
  index at any tick. Leave the input pending and retry on a later scheduled
  opportunity; do not record an automatic error, consume a failure retry budget,
  advance checkpoints, or spin waiting for the lock. Manual callers should
  explain that another capture is in progress. Other lock/I/O failures keep
  their actual error semantics.
- Keep previously valid evidence and catalog rows after partial failure.
- Healthy, independently committed groups or sources may still complete.
  Report partial results explicitly rather than marking the whole run current.
- A crash after evidence publication but before indexing must be resumable
  from retained evidence without needing the source to reappear.
- Keep source versions separate from capture time and parser version. Older
  captures must not overwrite a newer indexed copy merely because they were
  preserved or replayed later. Retain `guardCapture`/copy-authority semantics.
- Respect ingestion coordination, per-host capture locks, index locks, release
  cancellation and backup locking. Document lock ordering; do not deadlock by
  acquiring the manual-ingest coordinator recursively from a sync that owns it.
- Do not run a network upload while holding a source read transaction or a
  capture/index lock. Cloud backup remains an independent consumer.

Do not derive preservation success from `last_index_at > last_capture_at` or
its inverse. Source-level times are useful UI summaries, but exact retained
revision identities and committed dependency coverage are the proof.

Keep synthetic record/adapter fixtures usable in tests without creating a
production escape hatch. A test-only in-memory input is not a reason to add a
public `skip_capture` switch or silently accept unretained production adapters.

## Incrementality, performance and transition

The default sync interval is five minutes and can be as short as one minute.
A Conductor database in the investigated library was roughly 11 GB. Do not
quietly turn every timer tick into a full-library copy or full reparse.

For filesystem sources, reuse existing stable captures and skip unchanged
inputs. For SQLite, the durable exporter is a rollout prerequisite, not a later
performance optimization. A native snapshot of the investigated 11 GB database
needs database plus WAL plus the 1 GiB free-space reserve at acquisition; latest,
one prior and a possible unindexed extra can occupy roughly 22–33 GB before
temporary output/WAL growth. Constant Conductor writes make whole backups on
one-to-five-minute ticks unacceptable. Do not ship that bridge or implement
an indefinite pending state as its normal operation.

The SQLite cutover must demonstrate bounded candidate discovery, incremental
record/dependency publication, and periodic full reconciliation as specified
in handoff 1. Report the actual indexed observation time and last completed
reconciliation separately; a discovery tick is not proof every row was checked.
Skip unchanged payloads and share database reads across entities. Resource
exhaustion remains an explicit pending/error outcome with stale observation
time; it is not an acceptable steady state for the rollout performance gate.

Do not build a competing session format here, retain unlimited native snapshots
to simulate one, or disable the existing free-space reserve. Existing explicit
snapshot workflows keep their own measured, user-visible cost.

Do not use old index checkpoints as preservation receipts. Existing records
may be indexed but unpreserved; expose that distinction and leave historical
repair to handoff 3. A no-op index must not silently certify those records as
durably reproducible. Newly acquired content can become preserved without
proving that it reproduces an older indexed revision.

## UI, status and documentation

Keep **Index changes** convenient; users should not need to remember to run a
separate capture for safety. Reflect acquisition and parsing as distinct
progress phases in the same operation. Preserve stop/resume behavior and the
existing deferred-analysis distinction between manual and scheduled work.

Update source/capture status, automatic-sync history and MCP freshness where
needed. Separate at least acquisition success, indexed retained revision,
pending newer inputs, failures, and legacy preservation gaps. Do not mark an
unavailable source deleted or complete. A missing optional dependency and a
present-but-unreadable dependency must remain distinguishable.

Update `docs/incremental-sync.md`, `docs/configuration.md` and
`docs/technical-overview.md` to remove the current promise that Index changes
does not capture. Explain the remaining rotating-snapshot limitation until
the relevant export/backfill coverage is verified; do not announce
catalog-independent recovery early. During staged rollout, describe which
sources still use live ingestion rather than prematurely removing that caveat.

## Acceptance tests

Extend existing tests in `capture_test.go`, `capture_sqlite_test.go`,
`capture_index_test.go`, `capture_index_guard_test.go`,
`incremental_sync_test.go`, `incremental_recovery_test.go` and the appropriate
API/CLI tests. Use failure injection and bounded fixtures, not fixed sleeps.

| Scenario | Required result |
| --- | --- |
| Source changes between acquisition and parsing | Catalog contains the acquired revision; a later pass can acquire the newer one. |
| Source is deleted after acquisition | Parsing succeeds entirely from retained evidence. |
| Capture write, flush or manifest publication fails | No new catalog content/checkpoint for the affected input; prior evidence stays usable. |
| Process stops after durable capture, before indexing | Resume indexes retained data even with the original source absent. |
| Concurrent SQLite writer, including WAL-only commits | Acquired input is consistent; catalog content matches that exact input. |
| Concurrent capture replacement/pruning and indexing | Reader retains the revision it started with; markers do not certify another revision. |
| Parser version changes with source absent | Reparse uses captured evidence with original identities and no live filesystem fallback. |
| Metadata changes without a message append | Dependencies are captured and the affected group is reconsidered. |
| Source disappears or a dependency becomes unreadable | Explicit pending/error state; no deletion inference or false successful preservation. |
| Unchanged source and existing legacy checkpoints | Bounded no-op work; no false certification of historical evidence. |
| Another Mac's captures are replayed | Preserve originating host/source/origin and avoid local Git lookups for that Mac's paths. |
| Manual, automatic, CLI, repair and add-this-Mac entry points | Same acquisition-before-publication rule without nested coordinator deadlocks. |
| Capture/index/backup overlap or drive release | Existing locking, cancellation, space reserve and backup consistency remain valid. |
| Manual capture holds the host flock at an automatic tick | `errCaptureBusy` leaves work pending, records no automatic failure and advances no success/checkpoint; a later tick resumes. |
| SQLite cutover with a frequently changing large logical database | Incremental retained-record acquisition avoids whole snapshots per tick; reconciliation is bounded/cancellable and status reports its actual coverage time. |

Pay particular attention to the existing tests for reader stability while a
capture is replaced, older-capture guards, Conductor observed versions, group
membership, and capture/index writer identity. Preserve their behavioral
guarantees rather than deleting assertions to fit a new pipeline.

## Validation and delivery

Use `tools/setup.sh` on a fresh checkout. Run focused Go tests while iterating,
then `go test -short ./...`. Before a PR, follow `AGENTS.md`: `go test ./...`,
`npm --prefix web run check` and `npm --prefix web run test:full` with Node 24.
Use targeted browser checks for any changed progress/status UI; pure ordering
and state logic belong in Go/node tests.

Keep real libraries read-only during development. Do not invoke capture,
ingest, repair, compression or backfill on the user's library to validate a
fixture-based implementation. Use `testCatalog`, `testServer`, `t.TempDir()` and
the repository's fault-injection/durability harnesses. A user-authorized real
rollout is a separate operation after review of its scope and disk budget.

The cloud PR's full browser sweep had 59 failures reproduced on its master
baseline: two Activity refresh tests and 57 Query Table layout tests. All 79
cloud cases and required Go/frontend checks passed. Reassess the baseline when
working later; do not assume every future failure is pre-existing or patch
Query Table locally to make these checks green.

Deliver focused PRs with an explicit per-source cutover matrix. Land handoff 1
before SQLite enforcement and document the shared retained-input contract.
Definition of done for this workstream: all production imports of new source
content preserve and then parse the exact retained input, with honest
failure/status semantics. A filesystem-only first PR is partial completion;
historical rebuild coverage remains handoff 3.
