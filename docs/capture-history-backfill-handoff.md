# 3. Backfill and verify historical capture coverage


## Implemented prerequisite API (2026-10-23)

Handoff 1 now provides [SQLite row evidence v1](sqlite-records.md). Use that
specification's actual paths, commands, coverage receipts, budgets and locking
contract; the illustrative names below are not alternative formats. Automatic
SQLite cutover is still gated on hot 11 GB validation. Native/JSONL compatibility
and catalog backups remain enabled. No real historical backfill has run.

Use `preservation-pins.json` version 1, `sourceCapture.pinVersion`,
`releasePreservationOwner`, and source acquisition allowances (`gate`/`ungate`)
under the host capture flock. Pinning `files/...` first preserves immutable
history, including at retention zero. Capture manifest version 2 and catalog
schema 18 refuse incompatible writers. Do not use recovery IDs as leases.

Committed input receipts are in `records/v1/receipts/`; they identify the exact
retained path, input SHA-256 and verified revision. Cross-file/database closure
also needs the committed `groups/` receipt. `indexed.json`, `partial/`, and an
entity head alone do not certify historical closure. Do not pair an old database
with today's dependency files. Missing/unknown historical groups remain pinned
native inputs until the planner can resolve or report them. `sqlite-records
status|verify|replay` provide inventory, integrity checks and ordinary-parser
rebuild; `resolve` binds a legacy ordinal to an explicit revision. Persist job
cursors and rollback plans separately; those are not implemented by this format.


Status: implementation handoff, not an instruction to run a migration on the
real library immediately. Written 2026-10-23 after
[cloud backup PR #234](https://github.com/Pythia-Software/pharos/pull/234).

Implementation order: **3 of 3**, after
[1. Durable SQLite session captures](sqlite-session-capture-handoff.md) and
[2. Capture-before-index enforcement](capture-before-index-handoff.md).
For SQLite, durable session captures, replay and durable
pins must land before the SQLite cutover in
[preservation before indexing](capture-before-index-handoff.md); filesystem
enforcement can land independently. Build against their actual landed APIs,
pin mechanism and format; do not invent a second export format or treat the
existing recovery ID as input pinning.

## Objective and user decisions

Populate the new preservation layer with all recoverable history, including
sessions or structured metadata deleted upstream, and demonstrate what a fresh
catalog can reconstruct without the old catalog or source applications.

Cloud backups still contain the catalog. The user explicitly chose to keep
that design. Existing catalog rows are valuable surviving evidence, not
disposable cache to be wiped while testing the migration.

Backfill has two distinct products:

1. Source-faithful captures recovered from native files, retained SQLite
   snapshots/generations and new source acquisition.
2. Clearly labeled recovery exports of records that survive only in Pharos's
   catalog. These preserve what Pharos knows but cannot recreate unknown
   original columns, discarded raw payloads or already-lost revisions.

The final report must distinguish these products and unresolved gaps. Do not
claim a successful backfill recreates information no surviving source contains.

## Why historical backfill is necessary

Current captures retain ordinary source files after they disappear upstream,
and preserve rewritten file versions under `history/`. SQLite snapshots have
bounded generation retention, normally latest plus one previous snapshot.
Conductor-only salvage during indexing preserves sessions in the catalog, but
does not create independent raw captures that survive snapshot rotation.

The live incremental ingestion paths also historically allowed indexing
without capture. A source's latest capture timestamp and index status cannot
prove preservation of every historical indexed revision. Handoff 2 prevents
new sequencing gaps; it does not restore missing older evidence.

On 2026-10-23, a read-only audit of the user's actual library across two Macs
found the following, using existing native-alias links:

| Conductor provider | Indexed sessions | Sessions with a recognized Claude/Codex counterpart |
| --- | ---: | ---: |
| Claude | 4,962 | 979 |
| Codex | 2,505 | 2,439 |

The Claude gap was mostly older history. It included 2,458 unmatched Claude
sessions with at least 100 indexed messages. Recent Conductor sessions on the
investigated Mac used `~/.claude/projects` and `~/.codex/sessions` despite the
app bundling its own CLI binaries. Both locations were configured Pharos
sources, but native captures were last refreshed October 8 and October 15 on
the two Macs while indexing continued into October 22–23.

These are dated observations, not migration inputs or assertions for a test.
One counterpart link is an identity observation, not proof of complete or
equally recent message content. A matching file on the live machine is not
proof it was captured. Never assume Conductor is redundant with a CLI source,
or that every future Conductor engine uses the same storage locations.

## Relevant existing implementation

| File, table or subsystem | Use and caution |
| --- | --- |
| [`capture.go`](../internal/archive/capture.go), [`capture_sqlite.go`](../internal/archive/capture_sqlite.go) | Native capture manifests, retained versions/generations, source identity, durability and pruning. |
| [`capture_index.go`](../internal/archive/capture_index.go) | `captureView` remapping, host attribution, `IndexCapture` and Conductor `indexGenerations`. Salvage behavior is not a generic historical exporter. |
| [`capture_index_api.go`](../internal/archive/capture_index_api.go) | Captured-host/source inventory and index marker handling. |
| [`adapters.go`](../internal/archive/adapters.go), provider adapters | Parsed records and native IDs. Today's records omit raw fields; exporting these alone is a degraded fallback. |
| [`ingest.go`](../internal/archive/ingest.go) | Copy authority, `guardCapture`, observed versions and duplicate/mirror identity handling. Older backfill must not roll back newer catalog data. |
| [`incremental_recovery.go`](../internal/archive/incremental_recovery.go) | Resumable recovery-job/coordinator patterns and `repairRetained`. That repair recalculates derived data in the existing catalog; it is not an independent durable history export. |
| [`incremental_audit.go`](../internal/archive/incremental_audit.go) | Bounded reference-catalog comparison and retained-row reads. Its temporary audit directories are not migration output. |
| [`assets/schema.sql`](../internal/archive/assets/schema.sql) | Inventory actual catalog data and relationships rather than guessing a fixed list. |
| `workspaces`, `conversations`, `messages` | Surviving parsed entities and message records. Preserve native IDs, original order and selected/unselected variants. |
| `workspace_sightings`, `conversation_sightings`, `declared_native_aliases`, `conversation_identity_links`, `conversation_group_membership`, `copy_versions` | Original source/host/account identity, source-specific observations, mirror relationships, membership and copy authority. Do not flatten these into the current host. |
| `source_states`, `source_record_states`, `source_item_states`, `automatic_source_states` | Planning/history hints, not proof that corresponding raw evidence exists. |
| TL1 analysis, usage/accounting, changes, attempts, handoffs and other structured tables | Some information is source-derived but no longer recoverable from current native transcripts; inventory dependencies explicitly. |
| [`backup.go`](../internal/archive/backup.go), cloud archive code | Full catalog-plus-evidence backups and their existing fixture-based verification. |

Re-discover the schema at implementation time. Preserve explicit original
observations separately from parser-derived indexes. The catalog contains
Pharos-owned state as well as source-derived data: preferences/saved views,
bookmarks, finding decisions, accounts/hosts/source identities and related user
state require a documented preservation boundary. Cloud backups already keep
the catalog; this milestone must not falsely certify all user state as
reconstructible from session captures alone.

Do not copy Pharos cloud upload credentials, MCP authentication tokens or other
application secrets into per-session catalog-derived recovery envelopes. The
existing full catalog backup has its own agreed handling of those values;
source-history export is not an additional credential export mechanism.

## Phase 1: inventory and dry-run plan

Build an offline-capable planner before a writer. Inventory all captured hosts
and configured sources, native file versions, database generations, new-format
captures, and surviving catalog entities. Support source/host scope and a
stable bounded continuation mechanism. Do not discover only entities still
present in the latest native database.

Produce an explicit plan/report with:

- Available evidence versions and schema identities; existing export receipts.
- Entities recoverable from raw source evidence, from parsed catalog evidence
  only, from both, or from neither; include DB-only/shared/orphan records.
- Known missing, unreadable, corrupt, unsupported or conflicting inputs.
- Planned output/dependency bytes, temporary space, persistent retained-source
  pins and existing free-space reserve. Include the currently pinned latest and
  historical inputs, a concurrent snapshot allowance, temporary DB/WAL and
  scratch verification/catalog growth; report unknown sizes as unknown, not zero.
- Which operations write capture files, add migration bookkeeping or write a
  scratch verification catalog. Ordinary live indexes must not be rewritten
  as an unannounced side effect of exporting history.
- Effects on capture scheduling and snapshot pruning while inputs are pinned.
  State whether routine native snapshots are paused for the selected sources
  or how many additional rotations the disk budget permits. Every snapshot can
  move the prior latest into history while pins prevent reclaiming old inputs;
  ordinary latest-plus-one retention is not a bound during this job.
- A stable job/plan identity and criteria that invalidate the plan if relevant
  inputs change before execution.

Never write a single misleading “100% backed up” number based on row counts.
Count source-faithful, catalog-derived, partial, failed, pending and unresolved
coverage separately. A conversation can have complete message text but missing
usage or title evidence; track the useful scope of completeness.

## Phase 2: protect inputs and export surviving native evidence

Before historical export writes, protect the selected inputs with the durable
pins/concurrency gate below, then establish a verified rollback backup of the
catalog and retained evidence using existing facilities and a user-selected
destination. Pin bookkeeping is an explicit preparation write; protection must
start before a long rollback backup, not after its vulnerable inputs could have
rotated away. A dry run is not permission to perform a full copy of a large real
library. For production execution, make the concrete source, destination,
estimated space and execution scope reviewable first.

Use the format/writer from handoff 1 to export:

1. Every retained native database snapshot, including generations not useful
   to the current index parser and generations already marked indexed.
2. Native file history and new-format retained revisions needed to resolve
   session dependencies or close historical gaps.
3. Current live sources only through the capture-before-index acquisition
   boundary from handoff 2, if current acquisition is included in the selected
   execution scope. Never quietly read another Mac's original paths locally.

The order above is an inventory priority, not a replacement-selection rule.
Use handoff 1's first-class persistent pins; they are a prerequisite for writing
this migration. Under the per-host capture lock, reconcile the dry-run inventory
against current manifests, establish immutable identities for latest inputs,
durably pin each selected generation and its dependencies, then publish the
executable plan. A dry-run path listing by itself is not protected. If an input
rotated before pin acquisition, reconcile/re-plan instead of silently skipping
it. Do not manufacture a recovery ID to bypass retention.

Pins must be honored by `retain`, both deletion paths of `removePruned`
(including unnamed-history cleanup), rotation and ordinary capture. Confirm
writer compatibility before allowing overlap; an old binary that ignores pins
must be stopped or upgraded. Resume must rediscover pin owners from disk even
with the catalog unavailable. Release only this job's ownership, per verified
unit, after its receipt establishes dependency closure. Cancelled/failed jobs
keep their unresolved inputs pinned; abandonment requires an explicit safe
release decision, not a timeout.

Choose a concurrency policy in the reviewed execution plan. The default for
backfill is to pause routine **native snapshot acquisition for selected
sources** for the job duration, resuming the recorded prior scheduling state
on successful completion or safe cancellation. Do not pause independent sources
or new-format incremental capture unnecessarily. Manual Capture & index must
respect the same pause/budget gate and report pending, not bypass it. This
targeted pause is a backfill execution constraint, not the normal SQLite
indexing strategy prohibited by handoff 2.

Cancellation does not release unresolved pins. Resume the prior snapshot
schedule only after its capacity gate accounts for those remaining pins;
otherwise keep acquisition pending with an actionable status. Persist both the
pause's owner and prior state so restart cannot silently resume an unbudgeted
writer or leave an unexplained permanent pause.

If the operator elects concurrent snapshots, reserve a concrete byte/rotation
allowance for every additional prior-latest generation, snapshot temporary,
WAL growth and job output, above the existing free-space margin. Recheck the
allowance under the lock before each acquisition, including manual capture.
On exhaustion defer snapshot acquisition and report why; never evict protected
inputs or spend the free-space reserve. New generations must appear in an
amended inventory and be pinned/exported if needed for the claimed scope, or
be explicitly outside the plan's fixed cutoff. Do not silently expand the
completion denominator or let unlimited rotations invalidate the dry-run budget.

Export all distinct observations even when an older session is also in the
latest snapshot. The old observation may contain removed messages or metadata.
Deduplicate identical content/dependencies by hash while retaining provenance.
Use source observation ordering, not file discovery order or backfill time, to
choose a current complete observation. Preserve byte-identical original origins
and the file/WAL `Observed` versions required by handoff 1. Conflicting branches
belong in evidence only; they must not become parallel catalog authorities.
Unknown/equal conflicting ordering remains an explicit gap, with
`errOlderCopy` preventing non-advancing changed content from replacing the
catalog state. Preserve parser input ordinals and locator-to-row mappings too.

Missing referenced transcripts, inaccessible attachments, malformed protobuf
payloads, unknown schemas and orphan rows belong in the report. Preserve the
surviving raw bytes even when today's parser cannot interpret them. Do not
silently skip them and issue a complete export receipt.

## Phase 3: rescue catalog-only history

After comparing surviving native evidence with catalog observations, export
what the catalog uniquely retains. A catalog record lacking an exact raw
counterpart must not be dropped because a different provider mirror exists.

Use a typed, versioned recovery envelope supported by the new preservation
reader. It must identify itself as `catalog-derived` or an equivalent explicit
provenance tier, identify the exporting Pharos schema/parser versions and
capture the original host/source/account/origin/native IDs separately from the
Mac performing the rescue. Do not label it native SQLite evidence.

At minimum preserve, where present:

- Workspaces, repositories and retained source/location metadata.
- Conversation identities, aliases, sightings, parent/subagent lineage,
  model/effort/harness metadata, and membership relationships.
- Messages with roles, kinds, selection state, original IDs/order/timestamps,
  retained text/raw envelopes, evidence locators and accounting evidence.
- Source-derived structured observations not fully recoverable from messages:
  usage/model requests, attempts, handoffs, metrics with quality/provenance,
  retained change/PR evidence and TL1 task/candidate/flavor/event/review/human
  records. Inventory actual table relationships before finalizing this list.
- Completeness/missing-field flags and references to independently retained raw
  evidence. Preserve unknown source-derived data opaquely if it cannot yet be
  mapped, rather than inventing empty defaults.

Do not treat every derived cache or FTS row as original evidence. A serialized
database dump of all tables would reintroduce dependence on the whole catalog
schema; define a recovery contract that represents surviving records and their
relationships, with opaque fallback where needed. Derived ledgers can be
regenerated through the existing parser/writer if original evidence survives;
preserve values and provenance where regeneration is impossible.

Take consistent bounded catalog reads. A single hours-long read transaction
on the live WAL can prevent checkpoints and grow the WAL dramatically. Consider
a consistent rollback snapshot or bounded per-entity observations with explicit
versions and a reconciliation pass; budget the snapshot/temporary space and
pin relevant native inputs. The chosen strategy must not manufacture a
globally consistent observation from inconsistent table batches.

Catalog-derived fallback must never silently downgrade or overwrite a richer
native capture. Preserve both when they disagree and report conflicts. Keep
older catalog-only observations distinguishable from newer complete native
observations rather than guessing which has “more detail.”

## Phase 4: restartable publication and accounting

Use durable jobs, deterministic identities and content hashes. Resume after
process termination, cancellation, drive removal or a failed write without
duplicating observations or starting the entire library over.

The safe ordering per unit is:

```text
pin/inventory input
  -> read a consistent observation
  -> write and flush immutable evidence/dependencies
  -> publish committed revision manifest
  -> verify reachable evidence and persist completion receipt
  -> advance durable job cursor
  -> release input pin when safe
```

An interruption can leave extra durable evidence; it must not leave a completed
cursor referring only to an unpublished temporary file. Completion receipts
must include the input revision/schema/exporter coverage they certify. Reusing
an old job ID with changed inputs must trigger reconciliation, not a blind skip.

Keep progress queryable and stoppable: host/source, phase, entities/bytes
processed, output and scratch sizes, last checkpoint, skipped identical units,
failures, gaps and remaining work where known. Reuse existing operation and
recovery patterns where appropriate, but do not make the catalog the sole
inventory of successfully preserved files. A fresh catalog must discover them.

No production backfill should delete original snapshots/files, remove catalog
rows, reset source checkpoints, or switch ordinary parsing to incomplete output.
Destructive cleanup and retention changes are separate decisions after coverage
verification. All writes must respect the existing capture space reserve.

## Phase 5: prove rebuild coverage

Build a fresh scratch catalog using only committed captures and explicit
catalog-derived recovery envelopes. The original source directories must be
unavailable. The old catalog can be an oracle for comparison but cannot supply
missing records to the rebuild after the test begins.

Exercise the production replay/index path, identity linking and ordinary
derived-analysis refresh, not a special importer that copies the old indexes.
Where source evidence is native, re-run the provider parser. Where it is
catalog-derived, use the documented degraded recovery reader without claiming
raw-source reparsing.

Apply handoff 1's deletion policy to the oracle and rebuild alike. Sessions
salvaged after upstream deletion stay archived indefinitely using their last
accepted complete observation. In a surviving session, a newer complete
authoritative message set replaces the old set through `pruneMessages`; removed
messages remain historical evidence and must not reappear in the current view.
Do not compare a union of all historical messages with today's catalog or treat
a salvaged session's survival as a mismatch. Missing/partial acquisitions do
not authorize either form of deletion. Respect any explicit Pharos user-purge
exclusions separately.

Evidence preservation and catalog equivalence are separate report dimensions.
A changed equal/regressing-mtime branch may be fully retained but correctly
rejected from catalog application with `errOlderCopy`. If a legacy catalog
observation lacks an exact native revision association, preserve its chosen
state as catalog-derived and report that provenance; do not guess a raw branch
or promote its version just to make the oracle comparison pass.

Compare behavior and provenance, including:

- Recoverable session/conversation/workspace identity sets and source sightings.
- Retained messages, variants, raw payload hashes and ordering where available.
- Usage totals with accounting quality and mirror deduplication; model/effort,
  parent/child lineage, titles, tools and searchable retained reasoning.
- TL1 structured task/attempt/candidate/flavor/event/review/human information.
- Retained changes, PR relationships and metadata that does not live in JSONL.
- Search/query visibility and evidence-link resolution through public APIs.
- Explicitly excluded Pharos-owned state and any genuinely unrecoverable fields.

Raw content hashes and exact typed source values are strong checks when the
same evidence exists on both sides. Derived values may legitimately change with
a parser upgrade: classify those differences instead of blindly requiring
byte-for-byte equality of SQL tables or masking them with tolerances.

Report input population completeness and observed differences, not only matching
counts or aggregate costs. Equal message counts can hide changed bodies or
incorrect identities. A matching aggregate can hide mirror double counting.

Finally, exercise the existing local/cloud archive format with a small fixture
containing backfilled captures and recover it into an isolated location. This
does not require a real provider account or a real-library full upload.

## Acceptance tests

Use the Go harness and fault injection. Include at least:

1. A session deleted upstream and absent from the latest snapshot, but present
   in an older retained generation; export and rebuild it.
2. Multiple revisions where messages/metadata were edited or deleted. Preserve
   each observation without corrupting latest-state semantics.
3. A conversation present only in the old catalog, including meaningful usage
   or structured metadata. Rescue and replay it with degraded provenance.
4. Raw and catalog-derived versions of the same entity that disagree. Preserve
   both; prevent a fallback from replacing stronger evidence.
5. Two hosts with colliding native IDs, distinct accounts and original paths;
   aliases/mirrors and root/subagent groups must resolve correctly.
6. Antigravity DB-only/protobuf records and TL1 metadata/task records without a
   surviving native transcript. Do not omit them from the migration population.
7. Unknown schemas/columns, malformed bytes, unreadable dependencies, orphan
   shared rows and unsupported formats; retain evidence and report gaps.
8. Cancellation/failure at every publication/checkpoint boundary, then resume
   with the original source removed. Results must be idempotent.
9. Routine capture/index and backup running around a pinned generation. No
   referenced evidence may disappear, deadlock or become half-published.
   Exercise `retain`, queued deletes and unnamed-history sweeping, latest
   rotation with zero ordinary retention, overlapping pin owners and restart
   without the catalog. Pin release must follow verified export receipts.
10. Low disk space, stale plans, moved/unmounted volumes and changed source
    identities. Fail safely without changing the live catalog or pruning inputs.
11. A genuinely offline fresh rebuild with the old catalog absent, plus a
    separate comparison oracle. No live-source or old-index fallback is allowed.
12. A second successful backfill pass with no new observations. It should write
    no duplicate payloads and keep the coverage report stable.
13. A routine or manual Capture & index request during migration. Enforce the
    selected-source pause or concrete concurrent-rotation allowance, include
    newly retained prior-latest bytes and stop safely when the allowance is
    exhausted. Reconcile an input rotated between dry run and pin acquisition.
14. Byte-identical original origin keys, parser-ordinal locators and conflicting
    equal/regressing source versions. Preserve every evidence branch without
    granting it newer catalog authority or falsely resolving a legacy locator.
15. A deleted session alongside a surviving session with removed messages. Match
    the salvaged-session oracle while keeping the surviving session's latest
    accepted message set; historical messages remain accessible as evidence.

If Pharos has an explicit user-forget/purge mechanism, preserve its exclusions
through planning, backfill and replay. Upstream deletion is not a request to
purge the archive, but backfill must not resurrect data the user deliberately
asked Pharos to remove. Audit the actual supported behavior rather than
inventing a new purge feature as part of this migration.

Use small fixtures seeded in transactions, lowerable limits and `slowTest(t)`
for expensive safety tests. Do not write tests that freeze incidental SQL,
manifest formatting or UI DOM structure.

## Validation, real rollout and definition of done

Follow `AGENTS.md` for setup, focused tests, fast Go tier and required full
Go/frontend PR checks. Pure migration, ordering and reporting logic belongs in
Go/node tests; browser tests cover actual progress/focus/layout behavior only.
Do not work around Query Table dependencies with local patches.

Do not run a migration, full library snapshot, compression benchmark or real
cloud upload against the user's library merely because this handoff exists.
The user requested the work be specified and implemented carefully. A real
execution must have a concrete dry-run report, protected inputs, rollback
backup, disk budget, destination and resumable scope reviewed for that library.

Deliver a scoped dry-run planner, restartable export job, truthful coverage
report, offline rebuild verifier, operator documentation and the tests above.
Update the previous handoffs with the actual format/API details as necessary.

Backfill is complete for a selected scope only when every inventoried entity
has a verified committed preservation result or an explicit unresolved gap,
and the scratch rebuild demonstrates the claimed coverage. A report with
unrecoverable fields can still be a successful honest migration; those fields
must not be represented as restored or silently omitted from the denominator.
Retain the original catalog and cloud-backup behavior regardless.
