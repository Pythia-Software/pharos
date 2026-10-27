# SQLite row evidence, version 1

This is the SQLite preservation layer implemented by `capture_records*.go` and
`capture_pins.go`. It retains source data before provider parsing and reuses the
ordinary provider adapters for replay. The catalog remains in local/cloud
backups. No historical library migration runs at startup or as a test.

## Rollout

Managed-library builds write catalog schema 21 and capture manifest version 5.
These retain the schema-20 / manifest-4 preservation contract and also prevent
older writers from bypassing pending-library retention and delivery checkpoints.
Verified pending filesystem captures also retain a host-local `observed_ns`
witness, advancing when changed bytes are copied, so a same-mtime rewrite can
be delivered. Original `mtime_ns` and historical hashes remain intact. SQLite
keeps its original database/WAL authority and rejects conflicting equal versions.
Opening or restoring a library does not start historical backfill or enable
`retained_input_capture` / `sqlite_record_capture`; both remain explicit opt-ins.

Explicit **Capture** exports the captured SQLite databases into durable row
records. Existing JSONL files and native snapshot readers remain supported.
Conductor indexing also salvages sessions from durable row history after their
native generations pass retention. Indexing falls back to the record reader
when all native database inputs are unavailable, including logical-only captures
that have never created a native snapshot. `sqlite-records replay` selects
the record reader explicitly, including on a fresh catalog or another host.

Automatic live SQLite enforcement is **gated**. A Conductor source can opt into
`sqlite_record_capture = true` in its source configuration. Its changed-session
reads then go through `acquireConductorSession`: metadata planning, capture and
flush, reader pin, lock release, ordinary parser, ordinary catalog writer.
It makes no native database backup per session/tick. Initial/due reconciliation
reads rows in a bounded SQLite read transaction, without copying the database.
Sync installs the acquisition boundary before preparing its incremental plan and
keeps the resulting entity identities through execution. An unchanged file
preflight cannot skip an initial, overdue or incomplete reconciliation.
The default remains off until the hot 11 GB Conductor validation has passed.
TL1, Antigravity and Codex live callers have not switched to incremental logical
acquisition; explicit captures export their existing consistent snapshots.

The production-size gate is deliberately separate from small-fixture evidence.
Do not turn on capture-before-index enforcement by interpreting a partial export
or a native `indexed.json` marker as proof of complete preservation.

## On-disk contract

All authoritative files are below the source's existing `CaptureRoot` directory:

```text
<host>/<source>/manifest.json                  # capture manifest version 5
<host>/<source>/preservation-pins.json         # pins version 1
<host>/<source>/records/v1/
  objects/<sha256>.gz                         # immutable objects
  entities/<hash(database NUL entity)>/
    <revision-sha256>.json                    # immutable committed inventory ref
    head.json                                # atomic observation head, version 1
  groups/<hash(acquisition-id)>.json           # cross-DB/file closure receipt
  receipts/<hash(retained-path)>.json          # exact native-input export receipt
  partial/<hash>.json                         # incomplete, never authorizes pruning
```

Names/identities inside envelopes retain their original spelling. A source
namespace has a fixed host/kind/account/original root; configure a new source
name when that identity changes so prior generations cannot be misattributed. Native IDs do
not become path components. Object references give the SHA-256 and length of
**uncompressed** bytes plus `encoding: gzip`; gzip has a deterministic header.
Objects are content addressed and never automatically reclaimed. Source-state
integers and dependency nanosecond timestamps use decimal JSON strings too. Revisions have
`version: 1`, `writer: sqlite-rows-v1`, `encoding: typed-sqlite-ndjson-v1`, original
host/source/kind/account/database paths, entity key, acquisition time/identity,
original database/WAL source state, schema reference, table coverage, dependencies,
parent reference, original native capture time when available, completeness and a raw evidence identity. Format/exporter
versions are independent of provider extractor versions.

Schema objects contain typed NDJSON rows from `sqlite_schema` (including views,
indexes and triggers as **data**), followed by `user_version`, `application_id`
and database encoding. Tables retain `table_xinfo` column names, declared types,
primary-key positions, nullability, default expressions and generated/hidden
column metadata. An `implicit_rowid` column records the native SQLite row key
when accessible (including gaps); it is distinguished from declared columns.
Each row chunk is an NDJSON sequence of value arrays in column
order. Occurrence order is retained, including duplicate/keyless rows. Original
key values remain in the rows; a keyless occurrence is identified by its chunk
and sequence, never deduplicated as a set.

| SQLite value | JSON representation |
| --- | --- |
| NULL | `{"type":"null"}` |
| INTEGER | `{"type":"integer","data":"9223372036854775807"}` (signed decimal) |
| REAL | `{"type":"real","data":"3ff0000000000000"}` (16 hex digits of IEEE-754 binary64 bits) |
| TEXT | `{"type":"text","data":"..."}` (base64 of UTF-8 storage bytes, including invalid UTF-8) |
| BLOB | `{"type":"blob","data":"..."}` (base64 of exact bytes) |

NULL, empty TEXT and empty BLOB remain distinct. Embedded JSON/protobuf is never
parsed and reserialized as its only retained original. UTF-16 database encoding,
invalid identifiers, oversized rows and unreadable/unsupported tables fail the
logical export; their native inputs stay protected. Views/triggers/extensions,
DDL expressions and captured type declarations are never executed in replay.
Trusted code builds inert tables with quoted names, five trusted SQLite affinity
classes and bound values. Scratch
replay requires free space above the same 1 GiB margin and is removed on close.

Conductor `entity_identities` hashes the exact raw session rows (including
duplicates), original schema, message parser sequence and shared metadata. Full
and selective acquisitions of identical raw inputs therefore share session
authority while the whole database retains its own opaque/shared history.
A successful full reconciliation invalidates heuristic parts whose raw entity
identities changed, including edits with no changed signal.

Conductor `parser_inputs` maps each native session to a content-addressed table
sequence read with the **exact parser query** in the same transaction. It uses
`ORDER BY created_at,id` when available, and preserves the observed sequence
when ties/duplicates or missing order columns exist. The parser and locator
resolver consume that sequence instead of guessing SQLite row-key semantics.

## Coverage and source boundaries

Every full export enumerates every actual table, including internal/shadow and
unknown tables, and every readable column. `full` means a known table was fully
exported, `opaque` means unknown rows were fully retained, `selected` describes
an entity-scoped incremental selection, `excluded` is inventoried but outside
that selection, and `failed` is partial. Only a verified full scan can authorize
native generation pruning. Actual schema/table inventories live with each
revision; this matrix describes the current source families rather than claiming
that future app versions cannot introduce other relevant records.

| Source/table or dependency | Preservation/discovery |
| --- | --- |
| Conductor `sessions`/`workspaces`/`threads`, `messages`/`session_messages` | All fields, including alternate raw payloads, model/effort/sender/timing, aliases and cancellation. Incremental selection uses session ID; message parser order is retained separately. |
| Conductor linked `workspaces`, `repos` | All columns; shared metadata included in session acquisitions. Existing `conductorSignals` invalidates sessions on session/workspace/repository rows and indexed message statistics. Unknown/shared changes are discovered by full reconciliation. |
| Other Conductor tables, orphan rows | Fully retained opaquely on full reconciliation. Excluded explicitly from session-only acquisitions. |
| Antigravity `conversation_summaries`, all tables in `conversations/*.db` | All original values, including protobuf BLOBs/unknown fields. DB discovery is independent of surviving transcripts; DB-only and metadata-only sources are retained. |
| Antigravity transcripts, annotations, CLI logs | Original file bytes as immutable dependencies, with original paths/mtimes and upstream absence metadata. |
| TL1 tasks/attempts, transcript summaries, resource/tool usage, candidates, flavors, handoffs | Every row/column with original relationships, grouped by installation database. |
| TL1 `task_events`, `review_findings`, `human_responses`, `comments`, `agent_configuration_health`, all other tables | Every row/column; unrecognized tables opaque. Ordinary TL1 parsing rebuilds structured analysis as well as conversations. |
| TL1 registry, transcript and script logs | Immutable original dependencies plus retained installation/path mappings. External config/flavor files and code repositories remain intentionally uncaptured. |
| Codex supporting naming databases | All tables/columns alongside existing native rollout JSONL. No replacement JSONL format. |

A group receipt is published only after validating captured dependency versions,
repeating source discovery, and validating every database's source version.
Changes/new inputs/unreadable dependencies leave the group incomplete, without
a complete receipt. Absent databases retain their last verified revision and
are named explicitly in the group; source deletion does not mean Pharos purge.
An old cross-file database generation is not paired with today's transcripts to
manufacture historical closure; unsupported historical groups remain native
inputs for the later backfill planner. Rotation carries an already verified
export receipt to the immutable native history path. The receipt names that
retained path in `input` and the immutable revision's original path in
`revision_input` (omitted when identical); pruning rechecks the exact bytes and
original committed dependency group. Filesystem-only captures have no SQLite
group and keep their ordinary consistent-prefix behavior as transcripts append.

## Discovery, budgets and coverage status

Normal Conductor ticks retain the existing metadata/index prefilters. An edit
with no changed signal may be missed until reconciliation; an edit reverted
between observations cannot be recovered. No source triggers/schema/journal-mode
changes are installed. A full scan is due every 24 hours; source option
`sqlite_reconciliation_hours` can shorten this (1–24 hours). One due scan runs on
the next available tick. Explicit reconciliation forces a scan. Failed/busy scans
remain overdue and cannot advance the last successful coverage receipt.

Concrete acquisition defaults, per database consistent read/export:

- 600 seconds, configurable with `sqlite_capture_timeout_seconds` (up to 3600).
- 32 GiB of uncompressed serialized work, including dependencies and parser
  sequences; `sqlite_capture_max_bytes` may raise this up to 1 TiB.
- 5,000,000 row visits; `sqlite_capture_max_rows` up to 50,000,000.
- 512 MiB WAL growth, checked during reads at least every 250 ms and again at
  completion; `sqlite_capture_max_wal_growth` up to 8 GiB.
- 16 MiB total original values per row; a connection `SQLITE_LIMIT_LENGTH`
  bounds SQLite allocations as well as the SQL/driver row checks. Larger rows remain a native-snapshot limitation in v1.
- Approximately 1 MiB NDJSON chunks; one row can exceed the chunk target.
  Descriptor/schema limit 64 MiB; at most 100,000 Conductor parser-input entities
  per transaction. Limits are lowerable in tests.
- The existing 1 GiB free-space reserve applies to object output, compression
  finalization, pins/manifests and scratch replay. A stop retains completed
  chunks/partial inventory but never advances complete coverage.

Retrying a bounded consistent unit reuses durable objects; it does not stitch
unversioned batches from different transactions together. Impossible budgets
require raising the explicit source budget or exporting a consistent native
snapshot, not a successful no-op. Native historical inputs are never pruned to
make space for an unfinished export. Reconciliation status is in
`manifest.json.sqlite_record_coverage`, the capture API's `sqlite_record_coverage`,
and `sqlite-records status`. Status computes `overdue` from the last completed
scan and interval, even if an unavailable/busy source cannot write a new attempt.

Synthetic one-shot measurements on an Apple M3 Ultra (Go benchmarks, no real
library, 2026-10-23): full exports of 4/16/64 MiB took 0.138/0.337/1.484 seconds
(30–50 MB/s). Exporting one changed session with 4/16/64 MiB of cold messages took
59/51/89 ms and approximately 7.5 MB total allocated per operation. Total
allocations for full exports grow with input size; they are not peak RSS. These
small, compressible fixtures demonstrate the bounded selective path, **not**
completion of the 11 GB hot-database gate. Reproduce with:

```sh
go test -vet=off -run '^$' -bench 'BenchmarkSQLite(RecordExport|ConductorChangedSession)$' -benchtime=1x ./internal/archive
```

## Identity, replay, and deletion

Original origins remain byte-identical, e.g. `sqlite:<original Conductor path>`.
The revision reference is stored separately in `copy_evidence`, keyed like
`copy_versions`; TL1 installation analysis uses `source_evidence`. Both update in
the same catalog transaction as the accepted state. Catalog schema 20 refuses
older writers that would fail to maintain these associations.

Source mtime/WAL `Observed` remains the authority integer. A different raw
identity at an equal/regressing version remains evidence but is rejected with
`errOlderCopy`; a legacy equal version without an association is unresolved.
Identical evidence at equal version can be reparsed by a new extractor. Fresh
replay orders comparable native versions, refuses conflicting equal-version
observations, and preserves every branch. A head/export time is not authority.
Catalog-derived rescue/branch resolution belongs to the later backfill milestone.

A deleted session stays archived from its last accepted complete observation.
For a surviving session, a newer complete observation prunes removed messages
from the current catalog view. Prior rows remain evidence; replay never unions
old messages into a surviving session. Full raw-only rebuild does not restore
Pharos-owned configuration/user state; retain the catalog backup as designed.
There is no general user conversation-purge API to migrate in the current code.
Existing Ask Pharos native-session exclusions are still enforced by ordinary
catalog ingestion; their Pharos-owned state stays in the catalog backup.

## Locks, pins, and operator commands

Lock order: index lock, host capture flock, catalog transaction. Exporter temps,
object/inventory/head publication, native rotation/pruning and pin changes hold
the host flock. Parsers and uploads run after releasing it. Record objects are
immutable and have no automatic deletion. Reader pins cover their referenced
closure; a future collector must traverse active pins and committed inventories.

`preservation-pins.json` records immutable retained path/content hash/length,
owner, purpose, dependency paths, closure byte cost, creation and release state. Pinning latest first hard-links/moves
it to immutable history, even at retention zero. Overlapping owners release
independently/idempotently. Restart does not expire pins. Both queued pruning
and unnamed-history sweeps recheck them. Unverifiable/unexported generations
stay native regardless of indexed status or ordinary generation count.

Capture manifest version 4 is written/flushed **before** publishing pins or
capacity gates; version-1 and version-2 executables refuse it. Require all routine writers to
be compatible. A legacy native reader can still read its existing inputs with a
compatible writer; downgrading the executable is not supported after upgrade.

```sh
pharos --config PATH capture SOURCE
pharos --config PATH sqlite-records reconcile CONDUCTOR_SOURCE
pharos --config PATH sqlite-records status HOST SOURCE
pharos --config PATH sqlite-records verify HOST SOURCE
pharos --config PATH sqlite-records replay HOST SOURCE
pharos --config PATH sqlite-records resolve HOST SOURCE REVISION 'sqlite:...'
pharos --config PATH sqlite-records pin HOST SOURCE JOB RETAINED_PATH
pharos --config PATH sqlite-records gate HOST SOURCE JOB 0
pharos --config PATH sqlite-records gate HOST SOURCE JOB BYTE_ALLOWANCE
pharos --config PATH sqlite-records release HOST SOURCE JOB
pharos --config PATH sqlite-records ungate HOST SOURCE JOB
```

`replay` writes the configured catalog through ordinary ingestion. It takes the
same catalog-wide ingest file lock as the service before opening the catalog;
if indexing is active, retry after it finishes. A CLI replay also prevents the
service from starting another ingest until replay closes. Status and
verification do not open the catalog. Pin/gate operations are preparation
writes. Zero allowance pauses acquisition for that source; finite allowances are
charged durably before each file/snapshot/logical acquisition. The exporter
additionally reserves its entire configured serialized-work budget plus gzip
and metadata overhead before its first temporary; lower the source byte budget
when a reviewed job needs a smaller finite allowance. Unused reservations are
not automatically refunded. Crashes may waste allowance but cannot grant more
capacity. Status reports active owners, pinned
bytes (per-owner sum), partial acquisitions and remaining allowances. Owners
without a running job are not silently declared safe to release. Release only
after a verified closure receipt or an explicit safe abandonment decision.

The backfill planner/job and a production migration are separate work. Existing
native generations with unknown versions or unresolved historical dependency
closure remain protected for that work.

Capture-before-index also writes manifest version 4. `latest_records: true`
selects committed logical evidence for ordinary offline indexing even when an
older native snapshot remains. Earlier manifest versions remain readable; older
writers refuse version 4. Explicit successful native capture clears this flag.
Immutable filesystem inventories live under `inputs/<revision>/`; native reader
leases are temporary, excluded from backups, and reclaimed after a crash once
their per-reader lease lock is free.


Historical preservation uses the operator workflow in [capture-history.md](capture-history.md).
Capture manifest version 4 additionally guards durable job-owned native snapshot
pauses. `preservation-pins.json` stays version 1; `native_snapshot_pauses` is a
source-local overlay, independent of byte gates for all acquisitions. Backfill
catalog envelopes are explicitly degraded and never native SQLite receipts.

All SQLite readers use an OS-held lease, including live session and grouped
acquisitions. Closed readers are removed from pin bookkeeping. If a backup or
capture prevents that write, close reports deferred cleanup and leaves its
inactive lease directory as a cleanup queue. Status exposes these owners in
`reader_cleanup_pending`; they no longer pin evidence. The next capture or
replay retries the metadata write before reclaiming their directories. Active
readers and operator/job pins remain protected. Lease directories are excluded
from local and cloud snapshots.
