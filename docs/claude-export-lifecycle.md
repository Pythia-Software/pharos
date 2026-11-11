# Claude export storage and incremental import design

Status: proposed, 2026-11-11. This document is the design/measurement phase of
the Claude export compaction handoff. It does not enable a new source format,
migrate a library, delete downloads, or implement incremental Claude.ai parsing.
Implementation is split into the PRs below. The measurements use synthetic
exports exclusively.

## Decision

Use one portable, source-owned immutable object graph for both preservation
and incremental import. Retain each distinct message revision once, conversation
envelopes separately, and ordered references describing each observed
conversation. Retain nonconversation members independently as exact bytes.
Small export receipts record membership, source ordering, privacy filtering and
provenance. The managed import collection refers to the same preserved graph;
it must not hold a second permanent payload copy.

The preservation claim for eligible conversation JSON is **source-faithful
JSON under a versioned transformation**, not byte-exact ZIP preservation.
Unknown fields, arrays, all branches and message revisions survive. The ZIP's
compression stream, container layout, whitespace and object-key order do not.
Keep legacy bytes until the transformation and offline replay are verified.
Inputs that cannot be transformed without loss remain protected native inputs.

Storage granularity and parser granularity are different. Initially the parser
still receives a complete observed conversation, assembled from its source
objects. It parses only newly introduced conversation versions. A one-message
extension stores one new message plus changed metadata, but can still require
parsing a long conversation. Incremental message parsing would change branch,
tool pairing and authority semantics and needs its own measured follow-up.

## Verified checkout and corrections to the handoff

Inspected base: `c5f69b09`, including export preservation PR #669
(`982eb10f`). The handoff's preceding preservation work is present.

| Current implementation | Consequence |
| --- | --- |
| `claudeInventoryFormat = pharos-claude-export-v1`; import jobs require version 1 | New references need explicit versioned readers/writers, not overloaded v1 file paths. |
| `claudeAIExtractor = claude-ai-v4`; `Discover` validates then emits all inputs | A changed collection decodes historical conversations twice before record digests can help. |
| `Fingerprint` enumerates inventories and stats their files | An unchanged normal ingest can already skip parsing, including with a new adapter/catalog handle. Its metadata scan still grows with export history. |
| `collectClaudePart` copies/hashes input, checks ZIP members to EOF, then decodes conversations | A newly arriving ZIP must be read, even when its content later deduplicates. Existing collected parts are rehashed on job retries. |
| `readSupplement` is called during both discovery passes | Projects, memory, artifacts, reflections and cloud transcript documents are now indexed as well as retained. The handoff's conversations-only description and some configuration prose are stale; preserve these newer records. |
| `capturePlan.claudeAI` selects inventory and payload files | Capture holds another logical payload copy; retained immutable input inventories can protect additional versions. |
| `resolveAuthority` / `holdsEvery` give `user-export` conservative replacement | Omission cannot become deletion. Keep this writer and its message IDs. |
| `partTracker.unchanged` can skip older captures by version and size; tracker construction reads all source states | A naive part per conversation with a latest timestamp is unsafe for late historical observations and is not bounded discovery. |
| Plain capture manifests use version 5; this build understands encoded forms through version 8 | Reusing version 5 for the proposed graph is unsafe. Older docs mentioning only version 5 do not describe all current encodings. |

`TestClaudeImportFullThenOverlappingPartialExports`, cited by the handoff, is
absent in this checkout. Existing foundations include
`TestClaudeImportCollectsMultipartAndIndexesCapturedFiles` (including offline
capture replay), repair/CRC/privacy import tests, older/branch/account adapter
tests, and Downloads watcher tests. Do not cite a no-tests-matched invocation
as a passing regression. The synthetic probe below exercises repeated partial
exports and offline replay, but does not replace the implementation acceptance
tests in the PR plan.

Relevant contracts: [capture history](capture-history.md),
[compression](capture-compression.md), [SQLite evidence](sqlite-records.md),
[incremental sync](incremental-sync.md), [portability](portability.md),
[configuration](configuration.md#sources), and the
[existing export audit](claude-export-audit.md). Reuse locking, durable writes,
capture readers, pins, delivery and the ordinary ingestion writer. Do not put
JSON into `typed-sqlite-ndjson-v1` or treat catalog recovery as original evidence.

## Cadence, coverage and ordering

Support an optional full baseline, partial-only onboarding, a latest weekly
download, and a weekly batch of daily 7/30-day exports. Neither an initial
baseline nor every daily export is required. Preserve arrival order separately
from source observation order. An old conversation can be resumed at any time.

No authorized paired window exports were supplied with this task. The source
meaning of Claude's date filter remains **unverified**: creation versus update
filtering, inclusive boundaries/timezone, inclusion of complete message trees
versus fragments, and range metadata availability are implementation gates.
The earlier export audit establishes useful preservation examples, not a
rolling-window contract. Synthetic fixtures below deliberately model complete
conversation trees selected by recent activity; this is an experiment input,
not a statement about Claude's filters.

For authorized samples, compare a full export and overlapping 7/30-day exports
around a known old-conversation reply, a branch edit, and a boundary timestamp.
Inventory native IDs, parent edges, leaves, field shapes and source-supplied
range fields, without publishing conversation bodies. An older creation date
with a recent update should appear if the product expectation is satisfied.
Compare all message IDs/revisions with the broader export. A single sample can
disprove completeness; several matching samples do not prove a universal
upstream guarantee. Document the observed source version and keep unknowns.

Each receipt distinguishes:

| Fact | Authority |
| --- | --- |
| Requested range/scope | Source manifest if supplied; otherwise an explicitly labeled user report or unknown. |
| Source-declared cutoff and export creation | Exact source value, with its source field; never substituted with download time. |
| Actual included observations | Committed membership and native IDs, not a claim of continuous account coverage. |
| Download observation and capture time | Local events, separate from source time. A filesystem mtime alone is not a known download instant. |
| Collection completeness | Required nonexcluded parts verified, or missing/incomplete/unknown. |
| Tree coverage and continuous date coverage | Unknown unless separately established; absence does not imply deletion or a gap-free interval. |
| Index completion | Catalog instance/generation and extractor-specific checkpoint, separate from preservation. |

Retain absent/unknown fields explicitly; do not infer a last successfully covered
cutoff from a complete manifest. A seven-day weekly habit has little delay
margin; a thirty-day window tolerates more delay but cannot cover an arbitrary
interruption. An older export inside a newer range can contain unique revisions
or branches and must be acquired before deciding equivalence.

The current adapter emits `Coverage: "complete"` for each conversation; that
parser label does not establish upstream tree or date coverage. The new receipt
must not inherit it as proof. The format/parser PR must explicitly represent
unknown or fragmentary tree coverage and exercise the writer's partial-message
retention behavior, while keeping the existing conservative copy authority.

Preserve existing ordering inputs (`updated_at`, export creation and loose-file
source mtime) with their uncertainty. Collection sequence numbers are replay
cursors, never source freshness. Same-time conflicting observations remain
distinct evidence with an authority conflict; hash order must not pretend to
be chronology. Keep `user-export`'s add-only fallback. Do not claim that
arbitrary fragments now support all edit/reorder semantics. The implementation
must test late same-message edits and conflicting leaves against a full replay
before enabling destructive retirement.

The separate `claude-ai-settings` source remains outside this graph's export
coverage and reclamation scope. Model/effort/thinking settings are observations,
not execution usage or per-message model assertions.

## Storage alternatives

| Unit | Benefit | Limitation / decision |
| --- | --- | --- |
| Whole ZIP | Cheapest first-stage dedup of an identical verified download; retains exact filtered ZIP | Repacking and any daily change invalidate the whole blob. Keep as acquisition fast path and migration fallback, not final unit. |
| ZIP member | Reuses unchanged projects/assets; strips ZIP transport variation | A conversations shard or growing transcript can still be enormous. Use for opaque members. |
| Conversation | Straightforward reuse after shard/name/order changes | Every extension duplicates the full long thread. Measure as comparator, not default. |
| Message revision + envelope | Reuses stable bodies across conversation extensions; retains edits and branch selection separately | More objects, indexes and ordered references; parsing still needs conversation context. Selected JSON unit. |
| Content-defined chunks | Shares large changed fields and binary/text assets without understanding their semantics | Requires a specified splitter and additional references; chunking alone does not discover parser units. Use for large values after measurement. |

Distinct revisions legitimately grow storage. A new branch leaf or a changed
unknown field must create a distinct descriptor even if displayed prose is
identical. Equal hashes share bytes only; they never merge logical entities.
Do not use wall-clock timestamps, collection paths or map iteration order as
content signals.

## Proposed format: `claude-export-objects-v1`

This is a new format namespace; names below are proposed, not current paths.

```text
<capture_root>/<host>/<source>/exports/v1/
  objects/                         # immutable bounded packs and indexes
  receipts/<receipt-hash>.json     # immutable export membership/provenance
  observations/                    # persistent keyed tree of distinct observations
  commits/<sequence>.json          # immutable committed root + preceding commit
  head.json                        # atomic current root and sequence
  jobs/<job-id>/                    # resumable acquisition/migration protection
  retirements/<plan-id>/            # replacement proofs and deletion progress
```

The pool is owned by a host/source namespace. Account identity is part of that
namespace's immutable definition. No initial global or cross-host blob pool:
the smaller scope makes ownership, locks, backups and reclamation reviewable.
The same source's managed collection and captures share the graph. Other Macs
can read it from the portable capture tree without a live source or catalog.
Cross-host equivalent objects may remain duplicated; declare that limitation
instead of introducing distributed reference counting.

### Source representation

The canonical JSON transformation `claude-json-values-v1` specifies UTF-8,
sorted object keys, deterministic escaping, no insignificant whitespace,
preserved JSON value types, array order, null/empty distinctions and exact
number lexemes (`json.Number`, never a float64 round trip). Unicode text is not
normalized. Preserve all conversation envelope fields except `chat_messages`
in an envelope object. Each entire source message object, including its
unknown fields and nested content/tool/attachment metadata, is a message
revision. A descriptor references the envelope and ordered message revisions.
It preserves original message and parent IDs and selected-leaf fields.
The full conversation JSON can be reconstructed under that transformation.

Duplicate object keys, invalid UTF-8, unpaired surrogate escapes, or unsupported
shapes cannot silently pass through Go's lossy JSON map decoding. Detect these
before transformation; keep their filtered native representation and mark
compaction ineligible. The prototype only covers generated valid JSON. A
future syntax-preserving representation can broaden eligibility; v1 must not
claim it already does. Source field changes never disappear merely because
the current parser ignores them.

Large message objects/envelope values and opaque members use chunk references
once above a measured threshold (starting proposal: 256 KiB). The chunking
algorithm, min/target/max sizes and seeds must be frozen in a separately
versioned encoding before shipping; choose them in the format PR using asset
and long-tool-payload measurements. Until that gate is met, such inputs remain
native-only for destructive compaction. This prevents a superficially small
message count from hiding repeated giant tool payloads.

For nonconversation ZIP members, retain exact decompressed bytes, original
member name/order/type metadata, category and source hash. Include unknown
nonexcluded categories, empty members and supplementary metadata. Retain
cross-member dependencies in artifact `.files.json` inventories; do not
extract active HTML, follow exported paths or fetch missing attachments.
An attachment reference is evidence of a reference, not of absent file bytes.
Cloud transcript fragment boundaries/order stay in exact supplementary inputs;
do not apply ordinary `chat_messages` splitting to their different format.

Apply the existing account-record exclusions and `light_metadata` filtering
**before portable object publication**. Never copy source manifests containing
signed URLs to this store. Their credential-free allowlisted receipts preserve
names, categories, expected/actual hashes, sizes and safe source timestamps;
capability-bearing originals and signed URLs remain host-private. Do not
guess a generic scrubber for arbitrary conversation text or unknown fields.
Hashes describing original transport and filtered content are separate fields.

### References and receipts

An object reference includes kind/encoding version, SHA-256 and logical byte
length. Digest the domain-separated kind plus canonical uncompressed bytes;
compression is a physical encoding. Check compressed checksum/length and
logical digest when reading. Immutable packs have per-object offsets and a
pack checksum; publish only sealed packs. Indexes are rebuildable from pack
framing and committed roots. Bound pack size (initial proposal 8 MiB), handle
oversized objects explicitly, and do not put every tiny message in its own
filesystem allocation. Packing/rollover are publication mechanisms, not a
reason to change logical references.

Use persistent ordered sequences for message/member lists and a persistent
keyed observation tree, rather than copying a list of every historical
message/hash into every new receipt. A small append copies only the affected
bounded sequence pages and tree path. Preserve source order; do not turn
messages into a set. The prototype measures full descriptor arrays, so its
metadata savings do not assume the persistent-tree optimization already works.

A receipt binds:

- Stable existing host/source/account configuration and observed native account
  UUID separately. No identity inference from matching text or a filename.
- Original job/manifest hash and hierarchy; safe original part/member path
  spelling, source hashes, privacy filter version, missing/excluded parts.
- Envelope/message/member roots, source ordinals, logical lengths, observation
  version and the format/exporter version that produced the transformation.
- The coverage and time facts above; no invented range/cutoff fields.
- Original capture input revision and a versioned alias map for legacy evidence
  locators, source IDs and persisted paths. Source path labels stay labels on
  another host; never reinterpret them as live acquisition paths.

Do not alter existing conversation/message IDs, URL identities, origin encodings
or evidence locators to expose content hashes. Locator resolution translates an
old `ZIP-path:conversation:message` through the receipt/alias map. If an old
consumer requires an actual ZIP, materialize a temporary verified reconstructed
ZIP in its reader lease. This ZIP has explicit transformed provenance, not
the original ZIP digest. A consumer requiring byte-exact original ZIP evidence
continues to protect that native version; a locator alias is not byte equivalence.

Every accepted export has a small immutable provenance receipt. Payload
duplication settles for identical observations; receipt/membership metadata can
grow linearly with distinct exports. Initial policy retains all committed
receipts and source revisions. There is no automatic age/count cap. A future
receipt aggregation policy must preserve membership, times and audit identity
and needs a new reviewed contract. `claudeWatchKeep` is not that policy.

## Acquisition and incremental indexing

### Acquire once, commit evidence before index

1. Obtain the existing library volume/writer lease and import lock. Pin the
   job/input set, validate source identity, reserve space, and stage privately.
2. Copy/hash each newly selected download from its open descriptor, recheck
   mutation, expected size/hash and all ZIP CRCs, artifact dependencies and
   limits. Do not trust a basename, mtime or manifest claim as content proof.
3. If a transport digest has a committed compatible filtered-object closure,
   reuse it by indexed lookup. Check closure availability; a missing reference
   is a repair failure, not an empty export. Otherwise validate/split the new
   payload once and stream objects to bounded staging packs. The new ZIP still
   costs O(new received bytes); there is no free overlap detector.
4. Verify sealed new objects, flush with existing durable primitives, then
   under the host capture lock publish immutable receipt/observation-tree roots,
   commit record, and finally `head.json`. Publish the job's preserved checkpoint
   only afterwards. Concurrent acquisition rechecks the head and rebases its
   metadata; an existing hash must match bytes/length, never overwrite it.
5. Release capture/import locks before catalog parsing. A later index failure
   leaves preserved evidence and an outstanding index checkpoint. Missing parts
   remain incomplete; manual available-part indexing never marks a watcher job
   fully imported. Adding a late part creates a new immutable receipt revision.

An interruption before the head commit can leave unreferenced staging objects,
but cannot acknowledge preservation. A committed head is a durable root, not
a cached enumeration of today's managed folder. Rebuilding indexes/transport
lookups is explicit maintenance. Do not rescan all receipts during ordinary
collection, fingerprinting or planning. Deep integrity audits cost O(retained
bytes) and remain scheduled/manual maintenance, outside the small-import path.

### Adapter integration

Implement `partialAdapter` and `partialPlanner` on `claudeAIAdapter`, with a
v1 native path fallback. Source fingerprint is extractor + committed source
head + repair/force generation, not every historical receipt path. Plans
retain leased immutable roots and lazy readers, not decoded conversations.
Incremental groups are **distinct conversation observations**, plus the
existing supplementary document/cloud grouping with complete member dependencies.

Use an opaque item derived from stable namespace + observation root; never
use just conversation UUID + newest timestamp. An immutable observation's
part version is fixed; its signal includes the input root and transform version.
Different observations get different items, so `partTracker`'s older-size
shortcut cannot silently discard a newly arriving old branch. Preserve parser
epoch and force-generation invalidation. `Observed` on records keeps source
ordering; it does not use the fixed part version or acquisition sequence.

The existing planner/tracker is not sufficient unchanged: `prepare`,
`fingerprint`, and tracker construction can enumerate all groups/states.
Add a scoped/lazy state lookup for the newly committed frontier (indexed by
host/source/item/extractor), a durable catalog-instance cursor and a head-based
plan signature. An unchanged run reads a bounded head/checkpoint set. A new
export looks up only its observation delta and affected groups. Cold rebuild,
parser change and force generation explicitly enumerate retained observations.
Failing any group keeps its work pending; catalog commit and item state advance
together through the existing writer gate. Cancellation can flush only groups
whose catalog effects committed.

Separate content derivation from receipt application. Repeated identical content
can add a sighting/provenance receipt or a newer source ordering observation
without parsing that conversation again. Cached parsed records are derived,
keyed by observation root + extractor, and disposable. A source sequence A→B→A
must still apply the final A observation with its correct source authority,
even when A was parsed before. A globally remembered "hash already indexed"
bit is insufficient. A late older A remains evidence without rolling B back.
Parser-relevant dependencies (branch selection, envelope metadata, tool context,
supplement group membership/order) participate in the derivation key.

`source_item_states`, record digests and source checkpoints belong to a specific
catalog/extractor. A portable acquisition receipt cannot declare a fresh or
lost catalog current. Missing state means replay. Reuse the full-conversation
parser and `ingestRecordContext`/copy authority; do not create a second merger.
Stable raw evidence identity is based on source unit roots, while original
input-revision associations and receipt sightings remain resolvable separately.
Adapt `retainedRecordIdentity` deliberately rather than substituting a new
collection path or one whole-inventory digest for every record.

| State | Proof | What it permits |
| --- | --- | --- |
| Preserved | Committed receipt and verified durable source closure | Offline replay; does not imply indexed. |
| Indexed | Catalog transaction + source item/observation checkpoint | Current catalog has applied this observation. |
| Current for parser | Indexed proof matches extractor/force generation and dependencies | Skip derivation; does not certify disk integrity. |
| Eligible native replacement | Verified equivalent replacement, aliases, roots and compatibility gate | Plan retirement; index success alone never authorizes deletion. |
| Unreferenced source object | Complete locked root traversal and no protected owner | Potential future object GC; initially retain all committed objects. |

Warm indexing checks the small committed metadata/root boundary and availability
of newly referenced dependencies; actual readers verify object digests. It does
not claim unchanged old blobs were rehashed. Same-size external corruption of
an untouched object can be found by periodic verification/repair or the next
read. Integrity status reports last verified scope/time and pending audit, not
"all evidence verified" after a fast-path skip. A known corrupt/missing
dependency invalidates affected index/retirement proofs and retains originals.

## Ownership, migration and reclamation

### Protection roots

The collector must enumerate all of these, with error/unknown meaning protected:

| Root | Required lifetime |
| --- | --- |
| Committed export receipts/observation roots | All accepted distinct source evidence, including unindexed revisions. |
| Current capture manifest and immutable `inputs/<revision>` inventories | Exact original dependencies and alias mappings; rewriting a current manifest does not release an older inventory. |
| Active reader leases and preservation pins | Existing OS-held lifetime contract; unknown liveness cannot become absence. |
| Incomplete import/acquisition/migration/repair jobs | Verified partial work and all unconverted dependencies until explicit resolution. |
| Backfill, rollback and backup/export jobs | Preserve the exact closure promised by their receipts and in-progress readers. |
| Delivery checkpoints and pending work | A source unit must remain transferable/replayable until acknowledged under the delivery contract. |
| Other hosts/source namespaces | Never infer ownership from byte equality; do not reclaim their paths. |
| User's downloaded files | User-owned by default, entirely outside library GC. |

Existing SQLite pins name native paths/dependencies, not arbitrary object DAGs.
Extend their resolver to pin a graph root and its closure or explicitly enumerate
sealed pack dependencies. Never silently treat an unrecognized new pin kind as
empty. Existing backup/export/history/verification readers all need graph-aware
closure enumeration before native retirement is available.

### Compatibility gate

Use new job/inventory versions and a new capture encoding version above 8
(9 if still unused at implementation). Audit every reader/writer including
delivery, compression, immutable-inventory freezing and history export.
`writerVersion` must retain the new minimum while graph references or retirement
proofs exist; a subsequent plain-file write must not downgrade it.

A capture-only bump is insufficient to promise that an old collector cannot
create another job in `.claude-imports`: collection acquires the library lock
before reading a particular source. Before destructive migration, introduce a
library-level refusal recognized by existing binaries. The current library
descriptor reader in `library_registry.go` requires `version == 1`; a descriptor
version 2 upgrade can provide that hard barrier. Treat this as a coordinated
library upgrade, with version-1 read compatibility in the new executable and
tests proving old import/capture/backup entry points refuse version 2 before
writing. Reserve the number at implementation; do not quietly bump unrelated
libraries. Catalog schema/extractor bumps alone are not a portable writer gate.

Stop older writers and obtain the existing installation/library write ownership
before publishing the barrier. Running processes that loaded a descriptor
earlier must be excluded by the writer lease. Failure to establish the barrier
or prove every supported entry point refuses keeps migration additive-only.
Mixed-version operation then retains v1 payloads; downgrade after retirement
requires restoring an independently verified legacy backup, not lowering a
manifest's version number.

### Resumable migration

1. Plan only, initially. Bind the volume/root identity, manifest/inventory hashes,
   input hashes, pins, transformation version, supported readers and required
   space. Report unique/redundant/native-only bytes by layer; do not read live
   Claude Desktop data or request a fresh full export.
2. Protect each original closure and make the required rollback backup using
   existing backfill ownership. Publish the compatibility barrier before any
   graph-only state. Jobs are deterministic from their bound inputs and retain
   their ID/cursor on resume.
3. Stream legacy inputs into source objects. Verify reconstructed transformed
   JSON and exact opaque members against independent input reads, then run
   offline reconstruction through the ordinary parser/writer in an isolated
   catalog. Compare IDs, source values, branches, evidence locators, source
   ordering and supplementary records, not only counts or search results.
4. Publish durable replacement receipt, aliases and complete consumer/reference
   transfer proofs. Checkpoint only after verified publication. Until then both
   representations remain. Inputs that cannot be proven equivalent stay pinned
   with a concrete reason; migration can still finish useful independent units.
5. Produce a reviewable retirement plan. It may unlink a managed/native file
   only when *every* exact-path owner has either moved to an equivalent graph
   root or still owns a verified independent native copy. Historical immutable
   inventories keep their original identity; do not rewrite their hashes.
   If this cannot be represented by a versioned resolver, those originals stay.
6. Under the same library/host ownership, revalidate plan bindings and reader
   state, durably publish the retirement intent, then unlink exact owned paths
   one by one with durable progress. A crash after unlink but before checkpoint
   resumes by checking replacement proof and path identity. Never delete a
   different file that has since appeared at the same path.

Deletion failure leaves extra bytes and a retryable cursor, not a false completed
job. Read-only/unplugged/identity-changed volumes stop before mutations; never
recreate an unavailable library. Corrupt roots/indexes, unresolved pins, unknown
reader liveness, missing backups or unknown old-writer compatibility block
retirement. Index/catalog loss does not remove source roots. Object reclamation
and pack rewriting remain disabled in initial rollout; a later mark/copy/swap
collector must implement the same publication ordering and root barrier.

### Downloads and space policy

Default: preserve user-managed Downloads. No basename/glob/age cleanup. A later
explicit "remove verified downloads" action may name exact files in its preview
only when a durable replacement closure exists, all shared job references are
resolved, and open-file identity plus rehash proves the file is still the same
verified download. A changed file, symlink, unknown identity or unrelated hard
link ownership prevents automatic deletion. Reuse host-service primitives for
trash/identity mechanisms. Download deletion is separate from library compaction
and is not required for library deduplication.

Account for logical bytes, inode-deduplicated allocated blocks, and shared-extent
allocation as distinct measurements. `st_blocks` does not reveal unique APFS
clone extents. Report unknown allocation as unknown. Include import, captured
history, immutable inputs, packs/indexes/receipts, leases, backups, download
copies and scratch. The current 64 GiB collection budget is admission control,
not a retention policy; replace its whole-folder-per-part scan with indexed
committed usage plus reservations, periodically reconciled.

Before acquisition reserve the new transport, bounded staging packs, filter
rewrite scratch when needed, metadata journal and existing free-space margin.
Before migration reserve **all originals + new unique closure + required
independent rollback backup + largest bounded replay/verification lease +
metadata + reserve**. Do not count planned unlink savings as free space. On a
filesystem without hard links, budget lease/pin copies in full. Stream with
bounded memory; pause before a unit if its verified size cannot fit. A giant
ZIP member or artifact reconstruction may require more scratch than one pack.
Persist reservations under the writer lock so simultaneous jobs cannot both
spend the same free bytes. Record estimated versus observed peak; no current
probe value proves a migration peak bound.

## Measurements

Reproduce with `python3 tools/claude-export-probe.py` after workspace setup.
The script creates a temporary Go overlay with counters and an opt-in `probe`
test, runs the real collection, ingestion, capture and offline index code on
`t.TempDir()` libraries, and removes the overlay. It never instruments installed
Pharos or reads a user library. Raw results and logs go to `.context/`.

The fixture has a full baseline with 12 short conversations and one old long
conversation (128 messages). Each day adds a four-message conversation and
one message to the old long conversation. Exports select the previous 7/30
days' new conversations plus that resumed old conversation. Each synthetic
body contains 512 deterministic hex characters, with unknown fields and a
large integer to catch lossy reconstruction. Four weeks are measured as
latest-only weekly arrivals (5 exports including baseline), or four weekly
batches of seven daily arrivals (29 exports), indexed sequentially on arrival.
The probe then repeats an unchanged ingest, reopens the catalog with a new
handle/adapter, imports identical ZIP bytes with a new manifest, captures,
removes its scratch Downloads and managed collection, and indexes captures
into a new catalog.

The storage prototype writes actual gzip-compressed source message/envelope/
descriptor objects to one scratch pack, plus full JSON hash indexes and export
membership receipts. It verifies source-value reconstruction for each input
and calls the existing conversation decoder only for a changed conversation
root. Its round trip reads the in-memory object graph, not the pack on disk;
the sizing index uses placeholder offsets and logical lengths. Thus metadata
figures describe the prototype files, not the size of a working production
random-access index. It does **not** implement the proposed persistent sequences, durable
publication, supplementary-member storage, reference transfers, GC, parser
upgrade, or incremental catalog writer. Its parser counts include full changed
conversations. Prototype savings are experimental, not released performance.

Measured on this checkout with the synthetic fixture above. Byte counts are
logical stored bytes unless explicitly marked allocated. Storage is sampled
after the extra identical-ZIP/new-manifest import (6 or 30 receipts); decoder
totals cover the original 5 or 29 arrivals, excluding repeat/rebuild steps.

| Window / arrivals | ZIP payload in **each** legacy collection/capture layer | Prototype pack | Prototype metadata | Unique whole-conversation gzip comparator |
| --- | ---: | ---: | ---: | ---: |
| 7 days / latest weekly | 310,887 | 194,528 | 37,503 | 264,582 |
| 7 days / daily batch | 1,489,397 | 320,704 | 52,639 | 1,264,173 |
| 30 days / latest weekly | 385,428 | 194,528 | 41,724 | 264,582 |
| 30 days / daily batch | 1,787,654 | 320,704 | 69,523 | 1,264,173 |

The shared message payload itself is 146,806 gzip bytes in all four cases.
Daily long-thread versions make the conversation-sized comparator grow to
1,264,173 bytes. The prototype pack is 320,704 bytes, including 170,266 bytes
of compressed envelope/descriptor metadata plus framing. Message sharing helps,
but full reference arrays are already a substantial cost: persistent sequences
and packing need measurement before freezing the production format.

| Window / arrivals | Current cumulative decoder bytes | Prototype changed-root decoder bytes | Current conversation decode visits | Prototype conversation decode visits |
| --- | ---: | ---: | ---: | ---: |
| 7 days / latest weekly | 3,803,210 | 640,445 | 290 | 45 |
| 7 days / daily batch | 103,204,938 | 3,115,253 | 6,144 | 69 |
| 30 days / latest weekly | 4,239,330 | 640,445 | 430 | 45 |
| 30 days / daily batch | 114,234,922 | 3,115,253 | 9,686 | 69 |

These columns compare the existing two-pass reader with a prototype decoder
for changed roots, **not end-to-end import throughput**. Prototype acquisition
and canonicalization cost, catalog application and failure recovery are not
included in its decoder counts. The production collection counters separately
show the unavoidable initial ZIP validation/decode work.

For the last 30-day batch arrival, the current reader decodes 8,586,518 bytes
and visits 894 conversations; the prototype decodes 116,404 bytes and visits
two changed conversations. Current indexing across that batch hashes
22,831,902 archive bytes, performs 47,504,724 adapter ZIP read-at bytes, and
reports 488,723 catalog mutation rows. It skips 364 record writes after
performing the historical parsing. Those mutations are not 488,723 new messages.

Warm repeats and new-handle repeats decode zero conversations in all four
cases. They still open 5/29 inventory files and make 16/88 source stat calls.
Thus the existing unchanged fast path works; bounded metadata planning is an
additional requirement, not a claimed improvement already measured here.

The identical ZIP under a new manifest adds another 54,251 bytes (7-day)
or 79,088 bytes (30-day) to **each** legacy layer. The daily-batch index then
decodes 7,417,484 or 8,987,732 bytes respectively. The prototype asserts no
additional source object bytes; it appends a provenance membership receipt.

Allocated payload blocks in each legacy layer are 323,584 / 1,552,384 bytes
(7-day latest/batch), and 397,312 / 1,847,296 (30-day latest/batch). The prototype
pack allocation is 196,608 / 331,776 and 221,184 / 331,776 respectively.
Legacy collection/capture metadata allocated blocks are 49,152/36,864 for
latest-only and 245,760/167,936 for batches. Prototype metadata allocation is
40,960 / 57,344 / 45,056 / 73,728 in table order. These are observed stat blocks,
not unique physical extent ownership; do not sum them into an APFS claim.

All four production offline rebuilds pass after removal of the scratch
Downloads and managed collection: 41 conversations and 357 indexed messages,
including envelope metadata records. The prototype verifies transformed value
round trips only; it has not proved a catalog rebuild from a durable object
store. Peak temporary/migration space, true process restart and parser-upgrade
replay remain unmeasured release gates.


Counter definitions and limits:

- `decoded_bytes` counts bytes delivered to the actual conversation decoder,
  including validation and emission; `decoded_conversations/messages` count
  actual decode occurrences. They are not catalog row counts.
- `verified_archive_bytes` counts source part bytes delivered to the inventory
  hash verifier. `zip_read_at_bytes` counts actual random-access reads through
  adapter ZIP readers, including repeated reads/central-directory overhead;
  it excludes collection's direct ZIP reader. Neither is physical device I/O.
- Collection's `verified_expanded_bytes` counts member bytes checked by
  `verifyClaudeZIP`; its decoder bytes count an additional decompression pass.
  With conversation-only fixtures, index decoder bytes are also its expanded
  conversation bytes. Supplements require additional dedicated counters.
- `source_opens/stats` show metadata work even on an unchanged run. Directory
  enumeration, fingerprint JSON bytes, copy/hashing in capture, transient
  leases, OS cache effects and peak scratch/RSS are not completely instrumented.
- Layer totals distinguish ZIP/pack payload from all other files. Allocated
  figures deduplicate inodes within each layer only; they exclude filesystem
  metadata and cannot resolve shared clone extents across layers.
- `WriteRows` is the existing ingestion mutation counter, not just inserted
  conversations. `SkippedCurrent` and final conversation/message counts are
  reported separately. New-handle repeat is not a separate OS process restart.
- This is one deterministic tiny-text workload. Assets, very large tool values,
  native-only fallback, repackaging/shard changes, fragments and failure injection
  need the following gates before implementation or deletion claims.

For fixed repeated observations, the proposed payload converges while receipts
continue to grow. First/cold rebuilds necessarily read retained evidence;
new-indexer epochs must replay the same source contract. Periodic integrity
audits remain proportional to all retained bytes; pack/index maintenance and
future receipt aggregation have explicit separate costs.

## Phased PRs and release gates

| PR | Concrete edit points | Required evidence before advancing |
| --- | --- | --- |
| 1. Measurement and contract (this phase) | This design, synthetic overlay probe; verify `claude_import`, `claude_export`, adapter and capture contracts | Reproducible current baseline; honest prototype limits; authorized date-filter sample audit remains open. |
| 2. Additive source objects | New format/exporter/decoder; `claude_import.go`, `claude_export.go`, capture planner/fs/acquire; library/capture/job compatibility readers | Canonicalization adversarial fixtures, raw fallback, unknown fields/categories/assets, identity/locator equivalence, privacy before publication, independent offline replay, idempotent interruption recovery. Keep originals. |
| 3. Incremental application | `adapter_claude_ai.go`, `claude_export_records.go`, `ingest_parts.go`, `ingest_plan.go`, source state/frontier schema, capture-index/delivery integration | Delta-sized metadata lookup; unchanged/new-manifest/restart counters; parser/force invalidation; A→B→A and late A authority; no baseline parse for new export. Bump extractor and provider conformance index. |
| 4. Migration and retirement planning | Capture history/verify/pins/fs, compression, delivery, backups; resumable plan/verify command | Exact root inventory; reference transfer; old-writer refusal; restore/replay without originals or live catalog; deletion-boundary fault matrix; measured peak on copy-only and link-capable filesystems. Initially dry-run only. |
| 5. Reviewed reclamation and status | Existing import/source/job controls and progress APIs; source budget accounting | Exact-owned-path deletion with stale-plan refusal; unknown state protects data; retryable failures; storage/progress report separates preserved/indexed/verified/reclaimable. User-managed Downloads preserved by default. |

Do not combine these into the preceding export/settings PR. No Query Table
patches, new platforms, export request scheduler or Claude Desktop modifications.
UI changes in PR 5 require the existing control audit, site-map regeneration
and relevant browser checks. No UI/control changes are made by this design.

Extend the current import/adapter/download/capture/incremental test files;
ordinary cases stay small, larger synthetic measurement stays opt-in. Required
scenario matrix for PRs 2–5:

| Scenario | Gate |
| --- | --- |
| Partial-only, then optional full baseline; latest weekly 7/30-day window | Retained omissions, stable account/IDs, unknown coverage, broader merge. |
| Weekly batch, reordered arrivals, skipped days, long interruption | Apply new observations once; no old payload scans; no invented continuous coverage. |
| Same ZIP/new manifest and same content with changed shard/member order/name | No new large source payload; provenance preserved; identical source versions skip parsing. |
| Repeated giant thread extension, metadata-only edit, branch edit, A→B→A | Shared old messages; envelope/leaf changes invalidate correctly; source authority matches full replay. |
| Fragments, duplicate keys, Unicode/numeric edge cases, unknown fields/blocks/assets | Correct conservative merge or explicit native fallback; never lossy success. |
| Incomplete/corrupt/ambiguous multipart, cancellation/restart, concurrent acquisition | Reuse verified units, no committed dangling closure, no false watcher completion. |
| New process restart; parser/format upgrade; force generation; empty/lost catalog | Durable state gives warm savings; the right replay is invalidated; offline source-only reconstruction succeeds. |
| Kill before/after pack, receipt, head, pin, alias, proof, unlink, cursor writes | Previous accepted evidence readable; idempotent resume at every boundary. |
| Other Mac, backup, delivery, old immutable inventory, active/unknown reader | Every exact root survives; unknown compatibility/liveness prevents retirement. |
| Corrupt/missing blob/index, read-only/unplugged volume, no hard links, low space | Preserve originals, fail honestly, no mountpoint recreation, no optimistic space claims. |
| User Downloads changed/shared/unrelated files | No automatic deletion without exact verified ownership and explicit cleanup action. |
| Legacy migration with all downloads removed | No new full export needed; aliases and independent fresh-catalog equivalence verified. |

For source changes run focused tests and `go test -short ./...`; before any PR,
also `go test ./...`, `npm --prefix web run check`, and
`npm --prefix web run test:full`. Finish frontend builds before Go compilation.
The cross-compilation matrix stays opt-in. Do not turn the opt-in measurement
probe into a normal test or setup step.

Validation for this design/measurement phase: all four synthetic probe scenarios
passed; `go test -vet=off -run 'TestClaudeImport|TestClaudeAI|TestClaudeDownloads'
./internal/archive` passed; `go test -short ./...` passed, including the
architecture checks. Python syntax, new-file whitespace and local document
links were checked. Production source, source formats and frontend behavior
are unchanged by this phase.
