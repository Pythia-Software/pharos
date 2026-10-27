# Historical preservation jobs

`pharos history-backfill` inventories retained evidence and rescues catalog-only
history without rewriting the live index. It is an explicit, offline-input
operation. Installing this build does not start a migration. It does not finish
the SQLite default-rollout performance gates in [incremental sync](incremental-sync.md#source-acquisition-rollout).

A job has a fixed cutoff. It includes all selected retained native versions,
committed SQLite groups, immutable filesystem inventories, selected catalog
workspaces, and (for an unfiltered source scope) TL1 installation records,
including tasks without transcripts. It inventories unnamed native history, raw revisions lacking a committed group,
and incomplete SQLite acquisitions. Configured sources without captures
are gaps. No command in this workflow acquires from a live application's path;
another Mac's original paths remain identity labels.

The two preservation products are deliberately separate:

- Native SQLite row evidence uses [records/v1](sqlite-records.md), original
  database/WAL observation versions, schema, row occurrences, parser ordinals,
  hashes and the existing group/export receipts. Immutable filesystem input
  inventories retain their original paths and dependency versions.
- Catalog recovery uses the typed `catalog-recovery-v1` contract below. It
  preserves what the catalog knows and explicitly cannot restore discarded
  source columns, payloads or revisions. A provider mirror is not an exact raw
  counterpart. Recovery envelopes retain accepted raw references separately;
  those references are not new native-preservation certificates.

## Operator workflow

First stop older Pharos writers on every machine that can write this library.
Capture manifests now use **version 5**; earlier manifests remain readable, but
older writers must refuse version 5. Version 4 introduced backfill protection;
version 5 also protects pending-library retention and delivery checkpoints. Keep the library available at its checked
volume. Quiesce catalog indexing while planning and taking the rollback backup:
a changed catalog or changed selected bytes invalidates preparation. Native
snapshot acquisition for selected sources is paused durably during the job.
Independent sources and logical incremental acquisition retain their own
schedules. A busy library may require repeating the dry run.

All examples are templates, not instructions to run against a production
library without reviewing its concrete report and destinations.

```sh
pharos --config /LIBRARY/library.toml history-backfill plan \
  --host ORIGINAL_HOST --source SOURCE --limit 100
```

Omit either scope flag to widen that dimension. Follow `next_cursor` using
`--cursor` with the same scope. The cursor binds to the complete plan hash;
changed inputs require restarting the dry run. Pages contain at most 256 units.
The planner is read-only and cancellable, including while hashing input bytes.
It does re-inventory the scope for each page, so large snapshots incur read I/O.
The bounded inventory refuses excessive populations rather than clipping them;
use host/source scopes. Catalog reads use keyset batches.

Review every page and the catalog preservation boundary. The report gives
native bytes, existing pin costs (including other owners), catalog/WAL bytes,
the existing free-space reserve and free capture-drive bytes. Logical output,
compression ratios, full-library rollback-copy size, temporary/WAL growth and
scratch/catalog growth are explicitly unknown, not zero. Budget the **whole
library** rollback copy, including already pinned evidence, on its destination.
Native pause means zero additional snapshot rotations are authorized. There
is no concurrent-rotation election in this command.

Choose an absolute rollback destination on another physical drive, and explicit
output/scratch budgets in bytes. Preparation checks the existing backup
facility's destination rules, reconciles input identities, establishes durable
pins and source-specific native pauses, then publishes the executable phase.
It is a write operation even though it does not yet export history.

```sh
pharos --config /LIBRARY/library.toml history-backfill prepare \
  --host ORIGINAL_HOST --source SOURCE --plan REVIEWED_PLAN_ID \
  --rollback /OTHER_DRIVE/rollback-for-this-job \
  --output-budget REVIEWED_BYTES --scratch-budget REVIEWED_BYTES
pharos --config /LIBRARY/library.toml history-backfill resume JOB_ID
pharos --config /LIBRARY/library.toml history-backfill status JOB_ID
```

Resume takes a complete local catalog-plus-evidence backup using the existing
backup workflow, verifies the selected protected inputs in that destination,
checks the rollback catalog and records its hash. It exports from that
consistent snapshot, never from an hours-long live read transaction. Each
catalog entity is a bounded transaction against the snapshot; native SQLite
exports retain the existing row/time/WAL limits. Stop with Ctrl-C; resume the
same ID. Do not replace or update that rollback destination during a job.

Status reads bounded receipt metadata; verification rehashes the evidence.
Status separates source-faithful, catalog-derived, partial, unresolved and
pending **input units**. These are not interchangeable session/message counts
or a percentage backed up. Receipts and immutable payload hashes are rechecked
on resume. A failed unit remains pending; later attempts reuse committed
objects. A completed unchanged plan reuses its existing job. Unknown groups,
corrupt inputs, unsupported schemas and oversized entities remain actionable
errors/gaps; they are never silently removed from the population. Catalog
workspaces and TL1 installations exceeding the fixed 16 MiB recovery memory
limit receive durable unresolved receipts tied to the rollback catalog hash.
Other units continue, and resume does not retry these unresolved entities.
Increasing output or scratch budgets does not change this per-entity limit;
the full rollback catalog retains the oversized entity. Verification reports
the gap and does not attempt the same oversized comparison. Replay exclusions
must export successfully before a rebuild can proceed.

Output allowance is persisted before each writing attempt. Uncertain interrupted
attempts remain conservatively charged, so repeatedly failing cannot manufacture
new output capacity. Status reports that charge separately. If it exhausts the
reviewed budget, review space again and explicitly raise both recorded budgets:

```sh
pharos --config /LIBRARY/library.toml history-backfill budget \
  --output-budget REVIEWED_LARGER_BYTES --scratch-budget REVIEWED_BYTES JOB_ID
```

Budgets cannot be lowered below the recorded allowance. The capture-drive
free-space reserve still applies to every write, including abandoned objects.

## Offline rebuild and comparison

Rebuild always writes a new isolated catalog in an existing, mounted scratch
directory. An unavailable capture root fails planning; it is never treated as
an empty archive or recreated during resume. Original source directories and
the old live catalog may be absent. The normal provider parsers and ingestion
writer process native evidence; the degraded recovery reader handles catalog
observations. Rebuild never copies the old catalog's indexes into the new one. Enrichment
uses imported rows only; it cannot consult original transcript paths or newer
capture inventories, or create new captures. Headers absent from the selected
evidence remain unknown.

```sh
pharos --config /LIBRARY/library.toml history-backfill rebuild \
  --scratch /SCRATCH/new/catalog.sqlite3 JOB_ID
pharos --config /LIBRARY/library.toml history-backfill verify \
  --scratch /SCRATCH/another/new/catalog.sqlite3 JOB_ID
```

`rebuild` needs only committed evidence and receipts. `verify` performs the same
rebuild, then opens the separately hashed rollback catalog **after imports
finish**, for comparison. It reports exact content, variants/order, identities,
sightings, accounting and structured observation differences, as well as
additional archived workspaces recovered from raw history. Parser/accounting
differences are a separate category. Comparison samples are bounded; the full
difference count is retained. No matching count alone certifies equivalence.

Materialization stays on the selected scratch volume. Conductor history is
parsed one session at a time, with a 16 MiB retained-input budget per session.
Other SQLite groups have a 16 MiB parser-input budget. Oversized groups remain
preserved evidence and appear as parser-budget gaps. The scratch catalog/WAL
and free-space reserve are checked between imports. Verification interrupted
mid-build leaves the isolated catalog for inspection; a subsequent verification
uses a new scratch path. Export progress is retained independently.

Ordinary capture indexing also discovers committed catalog recovery envelopes
for the original host/source, including after a local/cloud archive is moved. Multiple differing
catalog-derived observations for an absent entity require an explicit job
rebuild/verification; ordinary indexing never chooses their authority from
lexical job order. Conflicts are reported only for recovery observations
matching the requested original host/source, so they do not block independent
sources.
Unreadable or oversized job metadata is skipped by ordinary captured-file
indexing and reported in `historical_recovery_warnings`; healthy native inputs
and readable jobs remain available. Explicit job commands still reject damaged
metadata. The affected job's historical coverage remains unresolved until it is
repaired and explicitly verified.
The explicit `rebuild` command additionally handles catalog-only sources that
have no surviving native capture target. Backups continue to include the
catalog and evidence; source-history recovery does not certify preferences,
bookmarks, findings decisions, cloud credentials, MCP tokens or other user state.

## Publication, pins and cancellation

Bookkeeping is under:

```text
captures/.history-backfill/jobs/PLAN_SHA256/job.json
captures/.history-backfill/jobs/PLAN_SHA256/receipts/UNIT_SHA256.json
captures/.history-backfill/jobs/PLAN_SHA256/verification.json
captures/.history-backfill/objects/ENVELOPE_SHA256.json
```

The plan/job identity is deterministic. It binds original source identities,
manifest hashes, selected retained versions, catalog observation state and
scope. Jobs persist their cursor, budget, rollback hash, protected input paths,
phase and errors on the capture drive. They can be rediscovered without the
live catalog. No receipt depends solely on a temporary file or an index marker.
Immutable envelopes deduplicate across identical observations; their receipt
carries the particular rollback snapshot's provenance.

The order is protect, rollback backup, immutable publication, verify, completion
receipt, durable cursor, then release of the corresponding closed input pin.
A crash may leave extra durable objects; it cannot produce a successful cursor
whose evidence was never published. All evidence/metadata writes preserve the
existing capture free-space reserve. Capture/reader locks are released before
catalog parsing. Jobs take a job lock before short host-capture locks; backups
run without an exclusive capture lock. No network upload runs under these locks.

`preservation-pins.json` remains version 1. The `native_snapshot_pauses` owner map
adds an overlay to the original schedule rather than changing schedule settings.
Other pin owners, capacity gates and schedules remain intact. A native pause
also protects source history from sweeping while unreadable/corrupt inputs
cannot be assigned a verified hash pin. Native/manual
Capture & index respects this pause and reports pending. Logical acquisition
does not spend a native-rotation allowance. Pinning on filesystems without hard
links makes a flushed immutable copy rather than moving away the latest file;
budget the additional bytes on those filesystems.

Historical cross-file/database closure is never guessed from capture-time
proximity or today's dependency files. Unresolved native inputs stay pinned,
even if raw rows were exported. An interrupted or cancelled job keeps its
unresolved protection. A successful verified job removes only its own native
pause when no unresolved pins remain; otherwise status explains why capture
is still pending. To explicitly abandon that protection after independently
reviewing the retained output and rollback backup:

```sh
pharos --config /LIBRARY/library.toml history-backfill release \
  --accept-unresolved JOB_ID
```

This is a separate operator decision. It does not delete evidence or certify
coverage. No backfill command prunes snapshots, deletes catalog rows, resets
source checkpoints, changes ordinary retention, or uploads the real library.

## Catalog recovery v1

An envelope declares format version 1, `provenance_tier: catalog-derived`,
exporter `catalog-recovery-v1`, exporting Pharos schema and recorded original
parser versions (or an explicit unknown flag), original entity ID,
entity kind, a `WorkspaceRecord` core, supplemental typed observations and
missing-field flags. Its hash covers the entire envelope; the committed job
receipt associates it with the consistent rollback snapshot hash. Export
machine paths and clocks never replace original source observations.

The stable core preserves source/account/native IDs, title evidence, repository
and location metadata, conversation model/effort/harness/lineage, ordered
messages, selection state, retained text/raw envelopes and evidence locators.
Supplemental observation kinds are explicitly allowlisted: original hosts,
sightings, versions, aliases, membership, accounting, agent/model/tool/skill observations,
instructions, work items/attempts/handoffs, metrics and their quality,
changes/files, PRs/relationships, and TL1 installations, flavors, tasks,
attempts, candidates, events, review findings, human touches and health records.
SQLite values keep NULL, integer precision, blob bytes and floating-point bits.
Unknown columns in these kinds remain opaque evidence. The exporting catalog's
SQL and indexes are never executable recovery instructions.

Recovery restores each message's exact recorded `source_order` from its typed
observation, including sparse values, duplicate positions from divergent host
copies, and NULL. The ordinary parser writer's sequential numbering does not
replace these observations. Search state and conversation documents are
refreshed using the restored positions.

Repository observations restore the original repository ID, aliases and local
locations directly, without probing those locations for Git metadata. The
workspace's repository association and location history are restored too.
The envelope also includes repositories referenced by the workspace's retained
PRs, even after the workspace changes repositories or no longer has one.
These dependencies are restored before PRs, while the core record keeps the
workspace's current repository association, including NULL.
Verification compares these values rather than certifying coverage from
workspace/message counts alone. An existing repository keeps its accepted
metadata; any difference remains visible in comparison.

Parent relationships include a bounded `conversation_parents` observation with
original child/parent conversation and workspace IDs, covering incoming and
outgoing links. Either workspace can be restored first. Missing links are filled
only for catalog-derived children once both endpoints exist; native children and
already different accepted parents keep their state. Missing endpoints remain
comparison gaps. Unresolved links for recovered children are committed atomically
to the catalog as pending derived state, survive restart, and are retried after
native capture indexing and identity reconciliation. A parent indexed later can
therefore resolve a child even when its native workspace already exists or no
recovery payload is replayed. Resolution retires pending state. Only the child's chosen observation can
create a pending link; incoming edges do not invent a parent for a recorded
root. Native children and already accepted parent links are never overwritten. Earlier v1 envelopes'
outgoing parent IDs use this same deferred resolution.

Agent sessions are restored before their nullable parent-session, delegation
message and child-conversation links. Missing endpoints leave durable pending
links committed with the selected recovery observation, so a delegation can
precede its child's workspace without failing foreign-key checks. Recovery
retries links by endpoint as more workspaces arrive; native capture indexing
and identity reconciliation also retry pending links in bounded batches after
restart. Only absent links on catalog-derived sessions can be filled; native
sessions and already accepted links keep their state. Unavailable endpoints
remain visible as comparison gaps.

The recovery core does not synthesize a default work item. Typed observations
restore the original work-item population and each conversation's exact
`work_item_id`, including NULL associations. Verification compares these links
as well as the work-item records, attempts and handoffs. Missing shared references
remain comparison gaps rather than receiving an invented default association.

Compatible supplemental fields are applied only to a newly recovered entity.
An existing native workspace or TL1 installation wins; conflicting fallback
observations remain independently readable evidence and are reported during
comparison. Missing conversations inside an existing workspace can therefore
remain a reported gap rather than authorizing an incomplete fallback to prune
or replace its richer native state. Catalog recovery sets
`preservation_completeness=catalog-derived` and does not create native
`copy_evidence` proof. Original copy versions/sightings remain distinct.

A fresh rebuild applies retained source observation order. Deleted sessions
stay archived, while newer accepted complete message sets prune removed
messages from surviving sessions. Equal-version conflicting native branches
remain evidence and cannot gain newer authority from export time. If a legacy
catalog's chosen state cannot be tied to an exact native branch, its recovery
envelope remains catalog-derived and the discrepancy is explicit.

The current code has no general conversation-forget/purge feature. Its existing
`ask_native_sessions` exclusion policy is exported as a small replay-exclusion
contract and applied **before** native parsing, so private Ask protocol sessions
are not resurrected into search. Ask settings, sessions and authentication
material are not exported into session envelopes.
