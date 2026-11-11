# Claude export source units and compaction

This implements the source representation, incremental payload parsing and an
explicit native-retirement command from the [lifecycle design](claude-export-lifecycle.md).
Automatic cleanup and bounded metadata discovery remain follow-up work. New
collections retain their filtered native ZIP/JSON files until an explicit plan
is applied. Downloads and Claude Desktop data are never modified.

## Preservation and layout

The collector first verifies and privacy-filters the native input using the
existing import rules. It then tries `claude-json-source-units-v1` retention:

```text
<capture_root>/.claude-imports/<host>/<source>/
  snapshots/<job>/job.json
  snapshots/<job>/claude-export.json
  snapshots/<job>/parts/<part>.zip       # retained until explicit retirement
  snapshots/<job>/units/<part>.json     # hash-bound source-unit receipt
  objects/<physical-sha256>.pack        # immutable gzip object packs
  .object-refs/<prefix>/<hash>.json      # disposable acquisition lookup
  .unit-heads/<identity-hash>.json       # observation continuity lookup
  .compaction/plans/<plan-id>.json
  .compaction/proofs/<proof-hash>.json
  .compaction/runs/<plan-id>.json
```

Each conversation envelope and each complete source message object is stored
independently. The envelope retains every field except `chat_messages`; the
receipt retains the ordered message references, source conversation ID, original
member name, source time and observation identity. Conversation-member membership
also records empty arrays. Directory entry names survive. Every other member,
including unknown categories and binary artifact files, retains exact decompressed
bytes, size and SHA-256. The existing account/sign-in and `light_metadata`
exclusions apply before this representation is published. Original URL-bearing
manifests and download capabilities remain host-private.

`claude-json-values-v1+exact-members` means source-faithful JSON values plus exact
opaque member bytes. JSON uses sorted keys, deterministic Go JSON escaping,
ordered arrays, no insignificant whitespace, unchanged Unicode and exact number
lexemes through `json.Number`. It preserves null/empty distinctions and unknown
fields. ZIP compression streams, entry headers, comments, timestamps, permissions,
container ordering, original JSON whitespace and key ordering are not retained.
Reconstruction never claims the original native SHA for its newly emitted bytes.
Duplicate JSON keys, invalid UTF-8, unpaired surrogate escapes, excessive nesting
or unsupported values leave the native input protected, with `source_units_error`
in its job status.

Object identities hash `claude-json-source-v1\0` followed by the source bytes.
A pack concatenates independent gzip streams and is named by the SHA-256 of its
physical bytes; receipts carry offsets, lengths, decoded lengths and object hashes.
Packs target at most 8 MiB. Values larger than 256 KiB use `gear-v1` chunks:
32 KiB minimum, a 64 KiB boundary mask, 256 KiB maximum, with a fixed SplitMix64
table seeded by `0x706861726f732d31`. Composite references retain ordered chunk
hashes and the whole value's identity. Readers verify sizes, gzip CRC/EOF, logical
hashes and reconstructed chunk identities. Cycles and unsafe reference paths fail.

The pool is local to one host/source. Equal content shares bytes without merging
logical conversations or accounts. Consecutive equal observations reuse parser
checkpoints even under a new manifest or shard name. A → B → A remains three
observations when source ordering distinguishes them. Edits to unknown fields or
branch selection produce new source observations.

## Incremental parsing

Claude extractor `claude-ai-v5` implements the existing partial planner and durable
`source_item_states` protocol. Only unseen conversation observations and changed
supplementary groups are read and parsed. Unchanged imports after reopening the
catalog perform zero source-object reads. A message extension stores new message
objects and an updated envelope; parsing still assembles the complete changed
conversation. Supplementary groups can span transport parts and are checkpointed
only after their full publication succeeds.

Changed inputs are validated before any of that pass's catalog records are
published. A cancellation midway through a native multi-record input never marks
the entire input applied. Force reindex, parser upgrades and a new catalog replay
retained evidence. The `user-export` copy authority and message identities remain
in place: omitted conversations do not delete history; a fragment cannot replace
a conversation unless it carries all stored message IDs. Older/equal-time
conflicting evidence stays retained and is subject to the existing evidence
promotion rules. This does not introduce general edit/reorder semantics for
arbitrary fragments. Tree coverage is `unknown`; collection completeness is not
full-account or continuous date-range coverage.

This version still enumerates historical inventories, reads their unit metadata,
loads historical item states and walks the collection for budget accounting.
Metadata/receipts grow with export history, and planning cost is not bounded by
only the latest delta. The follow-up needs a durable indexed frontier, lazy state
lookups and receipt/reference maintenance. The current improvement is reduced
payload parsing and deduplicated retained payloads, not constant-cost import.
No object/receipt garbage collector or automatic Downloads deletion is enabled.

## Review and apply compaction

Use the library's configured source name (default `claude-ai-imports`):

```sh
pharos --config /path/to/library.toml claude-import compact
pharos --config /path/to/library.toml claude-import compact --prepare
pharos --config /path/to/library.toml claude-import compact --apply PLAN_ID
```

The first command verifies existing replacements and saves a JSON plan. `--prepare`
also adds source units to legacy parts before planning. Both preserve native files.
Review the returned items and their `blocked` reasons before applying the exact
plan ID. `native_bytes` is the recorded logical native size, not filesystem blocks
or a promise of physical space recovered. Incomplete jobs remain protected,
including their available parts. An applied plan with blocked items reports
`state: partial` and `blocked_items`; otherwise it reports `complete`.

Apply requires the exclusive library-use lease; close services/readers using the
library first. It also takes the existing import and host capture locks. Plans
bind host, source, library ID/instance, capture-root identity, native SHA/size and
unit receipt SHA. Apply revalidates the native input, reconstructs every retained
source value/member and proves equivalence before publishing a retirement proof.
It checks library/volume identity again at publication and unlink boundaries.
Changed native data, missing/corrupt packs, receipts or proofs, wrong libraries,
unsafe paths and unavailable volume identity prevent retirement.

The ordered durable writes are: library compatibility gate, immutable proof,
versioned job, versioned inventory, managed native unlink, directory sync and run
progress. Retry with the same plan ID resumes interruption at these boundaries;
a missing existing proof is never silently recreated during resume. The run's
`retired_bytes` counts each logical part once. Explicit retirement does not depend
on the derived catalog as preservation proof and retains the entire source graph
for later indexing.

Apply unlinks only that host/source's managed `parts/` path. Captured native
copies, prior capture inventories, durable pins, backups and other hosts' paths
remain intact. Consequently, retiring managed ZIPs need not reclaim previously
captured native allocation. Immutable source packs share an inode with captures
where hard links are supported, using the existing native primitive; otherwise
capture uses a verified copy. No reference-counted pool reclamation runs here.

## Compatibility, readers and recovery

Additive units leave jobs/inventories at version 1 while native inputs remain.
A standalone copied v1 snapshot without its pool can still replay its native part.
Missing dependencies in a whole managed graph fail visibly.

Before graph-only inventory publication, explicit retirement upgrades the library
descriptor to version 2. Older writers requiring version 1 refuse it. Retired
snapshots use job version 2 and `pharos-claude-export-v2` inventories. A capture
with graph-only inputs sets the sticky `claude_export_objects` flag and manifest
version 9 before rotating/copying data. Readers understanding only version 8
refuse it. Recovery preserves the library's compatibility gate. Existing captures
without graph-only inputs retain their lower compatible manifest version.

Capture planning includes unit receipts, every referenced pack and retirement
proof. Original paths remain identity labels for records and locators even after
the native file is gone. `leaseCapturedEvidence` freezes the capture with the
existing reader lease, then reconstructs the requested input outside the capture
lock. Its result reports `Transformation`, the original filtered `NativeSHA256`
and an independently owned temporary `Path`; the caller must keep its lease open
and close it after reading. The legacy path-only helper still represents original
captured files and does not masquerade reconstructed bytes as an original ZIP.
The CLI's portable native-capture export scope remains Claude Code/Codex only;
library backups and capture indexing support the Claude.ai graph.

## Authorized sample observations and measurement

Read-only local samples supplied by the user were the all-time conversation ZIP
and recent 30-day conversation ZIP. Their filenames and private identifiers are
not stored in fixtures. The all-time sample contained 168 conversations and 912
source messages; the 30-day sample contained 21 conversations and 95 messages.
Shared conversations lost no message IDs, two source messages were added, shared
message bodies were unchanged and no dangling parent IDs appeared. No old
conversation resumed inside the window, so creation-versus-update filtering is
still unproven. Manifests supplied export creation and inventories, with no
range/cutoff metadata. No actual 7-day sample was available. These observations
are not a universal upstream coverage contract.

`TestClaudeSourceUnitsExportsProbe` is opt-in (`-tags probe`) and accepts explicit
`PHAROS_CLAUDE_EXPORT_FULL` / `PHAROS_CLAUDE_EXPORT_WINDOW` paths. It reads those
files, copies them into temporary directories, then performs every collection,
retirement and capture mutation in a disposable library. Source times in its
credential-free manifests are synthetic. No real content or identifiers are
committed. Latest measured conversation-only sequence:

| Operation | Conversation records parsed | Source-object reads |
| --- | ---: | ---: |
| Initial all-time export | 168 | 2,186 |
| Overlapping 30-day export | 1 | 22 |
| Same 30-day export under another manifest | 0 | 0 |

Those reads count object/chunk accesses, including preflight; they are not
physical disk-read counts or bytes. The catalog had 4,018 indexed messages
(including block/metadata records). Final unique pack bytes were **4,581,969**;
metadata including acquisition lookups, receipts, plans/proofs and run progress
was **595,932 bytes**. Three managed native inputs totaled **5,657,798 logical
bytes** (all-time 4,091,378; each window 783,210). Applying the plan removed those
managed native paths. The subsequent capture shared pack inodes, and a new
catalog rebuilt from graph-only captures with matching message IDs, text,
selection and evidence locators after the managed collection was removed.

Independent object compression can be larger than one monolithic ZIP: the packs
exceed this baseline's 4,091,378 bytes. Savings accrue across overlapping exports;
this is not a claim of savings for every first import. Plan/apply repeat full
verification and need temporary replacement space plus the normal reserve. During
migration native and graph coexist; on linkless filesystems captures/readers can
add full copies. Repeated identical exports stop adding large object payloads,
but retain provenance metadata and native inputs until another explicit apply.
Tests cover synthetic overlap, partial-only behavior through native merge tests,
repackaging, A → B → A, chunk reuse, cancellation, corrupt dependencies,
incomplete acquisitions, active library readers, link fallback, interruption,
legacy preparation, compatibility refusal and offline replay.
