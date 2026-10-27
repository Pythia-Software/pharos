# 1. Durable, replayable SQLite session captures


Implementation note (2026-10-23): the format, exporters/replay, durable pins,
retention receipts, authority mapping and opt-in Conductor boundary are now in
[SQLite row evidence v1](sqlite-records.md). Read its rollout gates and explicit
unsupported/budget cases before enabling automatic enforcement. This handoff is
the original acceptance contract; no real-library backfill has run.

Status: implementation handoff, not shipped behavior. Written 2026-10-23 after
[cloud backup PR #234](https://github.com/Pythia-Software/pharos/pull/234).

Implementation order: **1 of 3**. Land this before
[2. Capture-before-index enforcement](capture-before-index-handoff.md).
Filesystem enforcement can land independently. Implement the shared acquisition
boundary together, expose/export/replay retained records first, then switch
SQLite incremental callers to it. Do not force Conductor to take an 11 GB native
snapshot every sync tick or stop its indexing until this exporter exists.
[3. Historical backfill](capture-history-backfill-handoff.md) follows both
workstreams and the durable pin mechanism below. All handoff numbers refer to
this implementation order. Update dependent docs if APIs change.

## Goal and settled decisions

Preserve database-backed sessions as durable, source-faithful records that can
be consumed like retained JSONL transcripts. Deleting a session or metadata
upstream, rotating a native database snapshot, upgrading a parser, or losing
the original application must not remove Pharos's ability to replay the
evidence it previously captured.

The user asked for extracting every useful original detail before parsing,
with one logical capture per session/conversation or other appropriate source
entity. They did not ask for an export of only today's normalized messages.

Settled constraints:

- Keep the catalog in local and cloud backups. This is a preservation upgrade,
  not a switch to source-only backup or a cloud archive format redesign.
- Preserve raw rows/payloads, related metadata and necessary dependency files
  before running lossy parsers. Future parsers should be able to extract fields
  today's parser ignores.
- Retain edits/removals as historical evidence. Latest complete observation
  plus history is the model; largest message count is not a quality rule.
- Do not rely on native Claude/Codex mirrors to preserve Conductor. A real
  historical audit disproved universal overlap; see handoff 3.
- Read source applications only. Do not mutate their schemas, add triggers or
  change their journal mode to implement change capture.
- Do not run the real historical backfill in this milestone. Build and verify
  the format, exporters, readers and retention integration first.

## Current implementation and loss mechanism

[`capture.go`](../internal/archive/capture.go) retains native files under:

```text
<capture_root>/<host>/<source>/manifest.json
<capture_root>/<host>/<source>/files/...
<capture_root>/<host>/<source>/history/<stamp>/...
```

[`capture_sqlite.go`](../internal/archive/capture_sqlite.go) creates consistent
SQLite snapshots with the online backup API. The default retention is latest
plus one prior generation. Older generations can be removed once indexed;
there is only a bounded extra allowance for unindexed snapshots. The special
capture recovery ID bypasses normal generation pruning during that recovery
operation, not as a general historical-preservation contract.

[`capture_index.go`](../internal/archive/capture_index.go): `indexGenerations`
salvages Conductor sessions present only in older retained generations. Other
adapter kinds do not get that selective replay. In either case, an indexed
native snapshot can eventually disappear. The catalog presently remains the
long-term record of conversations removed upstream.

There is a separate byte-preservation concern: `queryMapsTransformContext` in
[`catalog.go`](../internal/archive/catalog.go) converts SQLite `[]byte` values
to Go strings. That is convenient for existing parsers but is not a lossless
BLOB serializer. Do not build the raw capture format by JSON-marshalling that
helper's output, `WorkspaceRecord`, or `MessageRecord` alone.

## Source inventory and scope

Start with a documented coverage matrix for every table and dependency in the
source databases Pharos captures. Conversation-only rows are not enough.

| Source | Current code and evidence | Preservation requirements |
| --- | --- | --- |
| Conductor | [`adapter_conductor.go`](../internal/archive/adapter_conductor.go), `database`, `session`, `conductorSignals`; tables selected from `sessions`/`workspaces`/`threads` and `messages`/`session_messages` | Session/message rows including alternate content fields, raw event envelopes, identity aliases, model/effort/sender/configuration, timing/cancellation, and linked workspace/repository metadata. Inventory additional related tables rather than copying only fields used by today's parser. |
| Antigravity app/CLI/IDE | [`adapter_antigravity.go`](../internal/archive/adapter_antigravity.go), [`adapter_antigravity_usage.go`](../internal/archive/adapter_antigravity_usage.go), [`adapter_antigravity_version.go`](../internal/archive/adapter_antigravity_version.go) | Full transcript JSONL, annotations, relevant CLI logs, `conversation_summaries.db`, and `conversations/<id>.db`. Preserve original protobuf BLOBs and unknown fields, not only decoded token counts or `gen_metadata` columns currently selected. |
| TL1 | [`adapter_tl1.go`](../internal/archive/adapter_tl1.go), especially `database`, `readActivity`, `task`, `tl1Snapshots` | Installation/task/attempt rows and relationships; summaries, resource/tool usage, candidates, flavors, handoffs, task events, review findings, human responses/comments and other relevant structured records. Keep existing registry, transcript and script-log mappings. |
| Codex supporting SQLite | Naming-database discovery and reading in the current adapter code; [`capture_plan.go`](../internal/archive/capture_plan.go) | Preserve database-held names/metadata alongside native rollout files. This is supporting evidence, not an excuse to duplicate or replace the native JSONL capture format. |

Antigravity's request DB contains repeated protobuf step content and usage that
is not equivalent to the visible transcript. Its `databases()` inventory is
currently driven partly by discovered transcripts. A database whose transcript
was deleted can be missed by that enumeration. Audit DB-only and metadata-only
entities explicitly so extraction does not inherit the same blind spot.

TL1 captures deliberately exclude external config/flavor files and code
repositories. Preserve database rows that describe flavors, but do not silently
expand capture to the uncaptured filesystem config or Git repositories. Record
those boundaries and missing dependencies honestly.

Sources may use different entity boundaries. A TL1 task with attempts, a
Conductor session with workspace references and an Antigravity root/subagent
group need not be forced into one conversation-only structure. Shared tables,
orphan rows and records without conversations still need a preservation path.

## Durable format contract

Choose a versioned envelope and a documented encoding. NDJSON with compressed
payloads and content-addressed dependencies is a reasonable implementation;
the extension and API names are not prescribed. The minimum contract is:

| Area | Required information |
| --- | --- |
| Format identity | Format version, writer/exporter version, encoding and compression identifiers. Keep these separate from downstream parser versions. |
| Source identity | Originating host, source name/kind/account, installation/database identity, original paths and source-native entity keys. |
| Observation | Acquisition/revision ID, source version evidence, observation time, schema identity, completeness and failed/missing dependencies. |
| Original schema | Relevant `sqlite_schema` definitions, columns/types/keys, source `user_version`/application identity where available, and enough typing to interpret each retained row. |
| Original rows | Table identity, stable row key or explicit keyless-row identity, all selected rows' columns, raw values and original ordering evidence. |
| Dependencies | Hash-addressed schema/shared-row bundles and retained transcripts, logs or attachments needed for replay; unresolved references must be explicit. |
| Integrity | File/chunk lengths and strong hashes, dependency closure, parent revisions and an atomic committed manifest/head. |
| History | Edits, row removals, session absence, prior complete observations and provenance; distinguish partial acquisition from authoritative absence. |

Preserve SQLite storage values unambiguously: NULL, INTEGER, REAL, TEXT and
BLOB. Large integers must survive JSON/JavaScript round trips without precision
loss; arbitrary BLOBs and invalid UTF-8 bytes must remain byte-exact. Preserve
the distinction between NULL, empty text and an empty BLOB. Include a defined
REAL encoding if exact round trips require it. Never parse and re-emit an
embedded JSON/protobuf payload as the only retained original.

Raw table names and schema text are untrusted data. Quote validated identifiers
when reading; do not execute captured DDL or source-provided SQL during replay.
If replay uses a transient relational view, construct it through trusted code
with extensions and unsafe SQL behavior excluded. Read-only replay of typed
rows is preferable to restoring an arbitrary application database and running
its triggers.

Include all original fields in each supported relevant table, including fields
not understood today. A schema inventory must distinguish fully exported,
intentionally excluded, unknown and failed tables. Unknown/unassigned rows need
an opaque shared capture or retained native snapshot; they must not be silently
discarded while the source is labeled complete.

“Every ounce” is an objective, not a claim of omniscience about future schemas.
Keep the native snapshot safety net where extraction coverage is unknown.

## Physical layout, identity and revisions

Keep the new evidence beneath `CaptureRoot` unless a concrete integration
reason requires otherwise. Existing backups already include that tree. Do not
create an unbacked sibling directory or store authoritative capture manifests
only in the catalog.

One possible layout is:

```text
<host>/<source>/records/v1/
  objects/<content-hash>...
  entities/<safe-entity-key>/head.json
  schemas/<schema-hash>...
  shared/<content-hash>...
```

This is illustrative. Use a hashed/escaped source identity for paths; never
interpolate untrusted native IDs as directory traversal components. Preserve
the original identity inside the envelope.

Exporter writes must participate in the existing per-host capture flock, with
one documented acquisition order and no recursive lock attempt when called
inside Capture & index. `removeCaptureTemporaries` recursively deletes every
`.pharos-tmp` file beneath the entire source directory at session start,
including `records/`. Existing `writeFileDurably` also uses that suffix.
Hold the capture lock from temporary creation through durable publication;
do not reuse that helper outside its lock and assume the subdirectory isolates
the temporary. Release it before downstream parsing/network uploads, using a
persistent revision pin for reader lifetime. A future writer outside this lock
needs a distinct suffix, its own cleanup ownership and coordinated publication;
that is not the initial implementation. Busy means pending, as in handoff 2.

The logical capture must be independently replayable, but a hot session need
not be rewritten in full every minute. Immutable row/event chunks plus revision
manifests can share unchanged data. Identical content should deduplicate;
observation timestamps alone should not generate a new copy of an unchanged
multi-GB payload. Bound memory and stream large BLOBs/records where possible.

Use source-specific identity and ordering. Keyless/duplicate rows must not
collapse accidentally. Preserve conflicting observation branches in the
evidence store, without ordering unrelated Macs by wall clock. The following
catalog invariants and deletion policy are requirements, not later decisions.

### Origin, ordering and catalog authority

- Replay must emit **byte-identical original origin strings**. Conductor's
  `ConversationRecord.Origin` is `sqlite:<original live database path>`;
  `a.original(path)` supplies that path. `copy_versions` and
  `conversation_sightings` use origin as part of their key. Never replace it
  with a records-directory path, export URI, hash or a newly normalized path.
  `storedCopyVersion` would find no prior version under a new origin, letting
  `guardCapture` accept stale content. Preserve host, account, workspace and
  conversation identity as well; store the new revision reference separately.
- Existing Conductor locators end in a parser-order index, not a message row
  key: `sqlite:<path>:<table>:<session>:<field>:<index>`, with some suffixes for
  alternative payloads. Record the exact parser input ordering and map each
  ordinal/variant to its retained row occurrence and raw field. `session` uses
  `ORDER BY created_at,id` when both columns exist; without a total order,
  persist the observed row sequence, including duplicates. Recreating a DB and
  hoping SQLite returns the same order is insufficient. Keep old locator
  resolution compatible and bind it to an observation; an unresolved legacy
  ordinal must be reported, never guessed against today's rows.
- The evidence store can branch; the catalog **stays linear** per host,
  workspace, conversation and original origin. Its monotone `Observed` integer
  remains the source file/WAL mtime version described in `adapters.go`.
  Export time, revision hash, row count and branch head do not grant newer
  catalog authority. Preserve the source version; do not synthesize a larger
  integer to force acceptance or bypass `guardCapture`.
- A content change whose source version does not advance is retained as a
  distinct evidence observation but rejected from catalog application with
  `errOlderCopy`. Current `guardCapture` accepts equality assuming identical
  content, so the new replay/acquisition boundary must additionally compare
  the committed evidence identity for that authority key. Persist/rebuild that
  association separately from origin. Identical evidence at equal version can
  still be reparsed after a parser upgrade. For a legacy equal version without
  an evidence association, report unresolved authority and retain the new
  evidence without claiming it proves the old catalog input or overwriting it.
- On a fresh rebuild, apply comparable observations in source-version order;
  retain but do not promote conflicting equal-version branches. Where there
  is no unique catalog head to reproduce, report the ambiguity. A catalog-derived
  rescue can preserve the prior catalog's chosen state with explicit provenance;
  it cannot convert the branch into newer native evidence. Workspace-level
  versions and existing partial-copy guards continue to apply.

### Upstream deletion and replay policy

Preserve the current distinction: **deleted sessions stay archived; removed
messages do not reappear in a surviving session's current view**. Conductor
`indexGenerations` salvages sessions absent from newer snapshots. Keep that
result indefinitely and give new-format readers equivalent session-retention
semantics for their supported sources. Session absence is an evidence event,
not a request to delete the archived catalog conversation. Replay the last
accepted complete observation for an absent session, preserving older history.

For a session present in a newer complete, authoritative observation,
`pruneMessages` removes messages absent from that observation's retained set.
Store those messages in prior evidence revisions, but do not union all revisions
into the current conversation or inflate usage with deleted variants. Partial
acquisitions cannot establish absence at either level. Per-row deletion must
be detected through the reconciliation rules below. Handoff 3 must use these
same semantics when comparing a rebuild with the old, salvaged catalog.

Distinguish a source-deletion observation from an explicit Pharos user request
to forget/purge retained data. This work preserves upstream deletions; it must
not silently defeat an existing intentional purge by resurrecting purged
evidence on the next replay. Inventory any existing purge semantics and carry
their tombstones/exclusions into the reader contract where applicable.

## Acquisition, publication and replay

1. Discover changed entities/dependencies using the bounded fast path and full
   reconciliation below. No per-table heuristic is proof of complete coverage.
2. Read relational evidence in one consistent read transaction per database
   or from a consistent native snapshot. Different databases and transcript
   files do not share a transaction: validate their versions around acquisition
   and retry or mark the group incomplete when they move.
3. Serialize original rows and dependency bytes before downstream parsing.
   Implement the shared capture-before-index boundary from handoff 2 here so
   it is ready before SQLite callers switch. Do not give a parser an
   independently reopened live DB.
4. Publish immutable data and manifests durably. Commit files/dependencies
   before the entity head, and the entity head before its catalog checkpoint.
   Reuse existing full-device-flush/atomic-manifest guarantees.
5. Parse a pinned committed revision with the ordinary provider parser and
   catalog writer. A failed parser must leave the evidence available for a
   future extractor, not remove it as unused temporary data.
6. Record the exact evidence revision in parser/part state and sightings or
   equivalent mappings. Make evidence locators resolvable to a retained table,
   row and column/payload; old `sqlite:` locators need a compatible resolution
   path rather than pointing at whichever live row exists today.

Preserve original host/account/source/native identities and observed versions.
Current capture readers deliberately map retained bytes back to their original
origins. Changing physical format must not create duplicate conversations,
break mirror links/parent lineage or grant a recovered record stronger
copy-authority than its original evidence. Enforce the exact keys and
non-advancing-version rule above, not merely an absence of visible duplicates.

Do not require the original application, original absolute paths, Git checkout,
network access or the source host to parse a retained capture. Reuse current
off-host behavior. Parser upgrades must re-read raw evidence and rebuild
messages, usage/tool ledgers and projections through existing code, rather
than maintain a second normalized-ingestion implementation.

A filesystem inventory and committed revisions must be sufficient to discover
captures after losing the catalog. Catalog indexes of evidence may accelerate
lookup but must themselves be rebuildable.

### Discovery, reconciliation and the bounded-work gate

Without source mutation/triggers there is no cheap universal per-row change
feed. Database/WAL signals identify a possibly changed database, not every
changed row. A table without a trustworthy monotone change signal requires
reading/hashing its candidate rows to detect arbitrary edits. Do not promise
both exhaustive per-tick detection and cheap discovery for every table.

Permit source-specific, per-table heuristic prefilters on normal ticks, such
as adapter signal columns and entity summaries. Document each table's signal,
what it can miss, and how related/shared-table changes invalidate candidates.
Hash typed rows and dependencies for selected entities; serialize only changed
payloads. **An edit without a changed signal column can be missed between full
reconciliations.** Even an edit followed by a revert before the next observation
may be unrecoverable. State this limitation; do not call heuristic discovery
complete preservation of all intermediate source writes.

Default full reconciliation is due at least every 24 hours while Pharos/source
is available, with one coalesced catch-up after downtime. Explicit Capture &
index also requests full reconciliation. Make the interval/configuration and
last completed coverage visible; an unavailable/busy source stays overdue,
not falsely checked. Full reconciliation reads every in-scope table/row,
including unknown/opaque/shared and DB-only records, verifies deletions and
dependency closure, and refreshes schema identity. Schema changes invalidate
the old table coverage claims and require reconciliation.

Use bounded memory, cancellation checks and per-run work/time budgets. Set
and document concrete production budgets in the implementation/PR, backed by
synthetic size-scaled measurements; do not hide them behind “resource limits.”
A reconciliation observation must be consistent per database: use a bounded
read transaction or explicitly budgeted consistent snapshot, never stitch
unversioned batches into a fictitious complete view. Long reads hold back WAL
checkpoints; budget WAL growth/time too. A budget stop keeps partial evidence
and resumable work but cannot advance the full-reconciliation coverage receipt.
An impossible budget requires an honest overdue/error status and an explicit
operator path, not an endless successful no-op.

SQLite automatic cutover requires evidence that a hot large logical Conductor
database can keep indexing changed sessions without a whole DB backup per tick,
while full reconciliation finishes within the documented resource budget.
Only explicitly requested or necessary consistent-snapshot operations may pay
the whole DB cost; unknown-table fallback must not quietly reintroduce it on
every tick. Preserve native snapshots as a safety net when coverage is unknown.

## Retention, compatibility and rollout gates

Keep existing native snapshot readers and capture manifests compatible. If
introducing a new manifest version, do not have an older writer unknowingly
rewrite it and drop new fields. Unknown formats must be refused safely with an
upgrade message. A parser version bump must not require recapturing originals.

Do not remove or aggressively prune old database snapshots as soon as the new
exporter exists. Handoff 3 still needs them. During rollout, guard any relevant
snapshot pruning against loss of an unexported generation. A catalog
`indexed.json` marker is not an export-completeness receipt.

The pruning gate must establish that the exact snapshot/generation's
relevant entities, shared rows, unknown-table fallback, dependencies and schema
were durably preserved and verified. Failed, partial or unsupported-schema
exports do not pass that gate. Coordinate reader/export leases with the
capture lock so concurrent capture cannot delete a generation being exported.

### Durable pins required before backfill

Implement first-class, on-disk preservation pins as part of this milestone;
the existing recovery-ID bypass is not a pin mechanism. A source-scoped pin
manifest (for example `preservation-pins.json`) records stable input identities,
immutable retained paths/content identity, dependencies, owning job/reader IDs,
purpose and release state. It must survive process restart and be discoverable
without the catalog. Choose/version the actual representation alongside capture
manifests; the example filename is not an existing API.

Create/flush pins and validate inventory while holding the same per-host capture
lock used by routine writers. Pinning a mutable latest path must first preserve
an immutable generation or establish a logical snapshot identity that rotation
atomically follows into history. A pin must also force preservation when the
ordinary generation count is zero. Path-only pins on `files/...` are unsafe.

`sourceCapture.retain` must keep pinned generations regardless of ordinary
retention/indexed state. `removePruned` must recheck pins both for queued
deletions and its unnamed-history sweep; pinned orphaned history cannot be
deleted merely because a current manifest omits it. Routine capture, rotation,
cleanup and exporter publication must all honor pins under the same lock.
Overlapping owners release independently and idempotently; one completed job
cannot release another reader's pin. Crash recovery must retain pins until a
resumed/completed or explicitly abandoned job proves release safe; do not expire
them on elapsed time alone. Surface abandoned pins and their byte cost.

Pins protect old inputs, not future rotation capacity. Routine captures may
create additional retained generations while backfill runs. Check the reserve
and the job's budget before every acquisition/rotation; defer capture safely
when capacity is exhausted without deleting a pin. Handoff 3 budgets this
growth and controls whether routine snapshot capture is allowed during the job.

Require compatible routine writers before enabling pin-dependent migration.
An older executable does not understand the pin file and can prune it away.
Version-gate capture writes with a reader/writer compatibility check; until all
writers honor it, quiesce incompatible writers rather than assume a new optional
manifest field protects evidence. Test downgrade refusal explicitly.

Do not introduce automatic deletion of unique session revisions in this work.
Deduplication is acceptable; reclaiming historical evidence needs a separately
specified policy. Preserve the capture free-space margin for all writes,
including compression finalization. If space runs out, fail/defer acquisition
honestly; do not prune the only evidence of an indexed session to make room.

Raw captures inherit the sensitivity and local permissions of existing source
captures. Keep secrets and conversation bodies out of status/logging. Capturing
conversation databases does not authorize copying auth/config directories.
Cloud credential storage and optional archive encryption stay as agreed.

## Acceptance tests

Use small synthetic SQLite/JSONL fixtures with deliberately unusual fields.
Extend existing capture, adapter, copy-authority and backup tests; add a focused
format test file if this is a genuinely new module, not a review-only file.

- Round-trip NULL/empty values, invalid UTF-8/BLOBs, large integers, REALs,
  nested raw JSON, unknown protobuf fields, duplicate rows and schema changes.
- Preserve an unknown column and later parse it with a new extractor while
  all original source paths are unavailable.
- Delete a Conductor session, Antigravity summary/request record and TL1
  task/attempt upstream; rotate native snapshots beyond their retention limit;
  reconstruct the previously retained history from new capture files alone.
- Exercise DB-only/metadata-only entities and shared/unassigned records, not
  just conversations whose full transcript still exists.
- Change or remove a row without appending a message. Keep old evidence,
  accurately represent the newer state, and avoid duplicating usage on replay.
- Change a row without advancing its heuristic signal. A fast tick may miss it;
  full reconciliation must retain it and report its actual coverage time. A
  budget-interrupted reconciliation cannot claim complete coverage.
- Replay an older Conductor revision using byte-identical origin/authority keys;
  it must be rejected. Resolve parser-order locators, duplicate rows and
  alternate fields after source removal without inventing row-key semantics.
- Retain conflicting equal/regressing-mtime content in evidence but return
  `errOlderCopy` on catalog application. Equal-version identical evidence must
  still support parser upgrades; legacy unassociated equality stays unresolved.
- Delete a whole session, then remove a message in a different surviving session.
  An offline rebuild keeps the deleted session, uses the surviving session's
  latest accepted message set and retains all older evidence without unioning it.
- Test cross-database/file acquisition races and present-but-unreadable optional
  dependencies; incomplete groups must not receive complete receipts.
- Inject interruption before/after object writes, manifest/head publication,
  capture receipts and catalog commits. Resume idempotently with sources gone.
- Hold a reader while capture/export/pruning/backup runs. It must see a stable
  revision and keep every referenced dependency alive.
- Start routine capture while an exporter is writing temporaries. Lock ownership
  must prevent `removeCaptureTemporaries` from deleting an in-flight file.
- Restart with persistent pins; exercise `retain`, queued and unnamed-history
  cleanup, zero-generation retention, latest-to-history rotation and overlapping
  pin owners. Reject incompatible older writers; stop on insufficient capacity
  without pruning protected evidence.
- Rebuild a fresh catalog on a simulated other host with no source paths.
  Check content, usage, models/effort, parent lineage, titles, aliases, tools,
  TL1 structured analysis and original sightings through public behavior.
- Preserve ordinary existing JSONL captures and old SQLite snapshot readers.
  Future/unknown format versions must fail safely, not look like empty input.
- Use large logical values and configurable limits to verify bounded work,
  cancellation and free-space failures without filling a real disk.
- Back up and restore a fixture containing new record captures. Verify replay
  from the restored copy, including optional cloud archive encryption through
  existing local/mock-provider test boundaries.

## Validation and deliverables

Follow `AGENTS.md`: Node 24, `tools/setup.sh` for a fresh checkout, focused
tests, fast Go tier for changes, and the full Go/frontend PR checks. Keep data
safety tests even when slow and mark slow fixtures with `slowTest(t)`.

Do not benchmark, compress, export or migrate the real library as an implicit
test. No real provider provisioning/upload is required. Real data may be used
only in a separately scoped read-only audit or an explicitly authorized rollout.

Deliver the versioned format specification, typed writer/reader, source coverage
matrix, discovery/reconciliation budgets, exact identity/authority integration,
durable pins, migration/retention gates and fixtures
proving offline replay after snapshot rotation. Document resource costs and
remaining unsupported schemas before enabling SQLite incremental enforcement.
Finish by updating handoffs 2 and 3 with the actual
format, commands, completion receipts and APIs, so backfill does not invent
its own interpretation of the new format.
