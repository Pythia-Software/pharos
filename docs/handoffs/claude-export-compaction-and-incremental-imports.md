# Handoff: Claude export compaction and incremental imports

## Assignment and scope

Design a unified compaction and import architecture for Claude Chat exports that:

1. Prevents repeated, overlapping exports from indefinitely accumulating duplicate
   payloads on disk.
2. Imports new exports without repeatedly hashing, decompressing, parsing, and
   reconciling the entire history of previously accepted exports.
3. Preserves source evidence, conversation history, resumability, and the ability
   to rebuild a fresh catalog offline.

Treat storage and import efficiency as one lifecycle problem. A cleanup job that
breaks replay, or an incremental parser that still requires keeping every
redundant ZIP forever, is not a complete solution.

This is follow-up work, **not implementation for the preceding Claude export and
conversation-settings PR**. Start from a checkout containing that merged work,
read the current `AGENTS.md`, and verify the symbols below against the checkout.
First produce a concrete design, tradeoffs, measurements, and phased implementation
plan. The format and compaction granularity are deliberately not settled here.

Creating or scheduling exports in Claude is outside this assignment. Users are
expected to arrange that themselves. Pharos currently collects and imports
exports that arrive; it does not request their creation or select their date range.

## Expected user behavior: design for this first

The expected common setup is:

- The user requests an export **daily**.
- Each export contains the last **7 or 30 days of chat activity**.
- The user downloads exports **at least once a week**, rather than necessarily
  downloading and importing every daily export immediately.
- A full initial export may establish a baseline. Later exports may be full,
  rolling-window subsets, or a mixture.

Support both weekly arrival patterns: downloading only the most recent available
export, and downloading a batch of several overlapping daily exports. Do not
assume that every requested daily export arrives, arrives in order, or arrives
before the next export is created. Repeated downloads, browser-numbered filenames,
retries, incomplete multipart transfers, restarts, and long interruptions are normal.
A full baseline must not be required to start using Pharos: partial-only history
should remain useful, with honest limits on coverage before the first known window.

The exact meaning and availability of Claude's date filters must be verified
against representative, user-authorized exports. The cadence above is the product
expectation, not a claim that the importer already understands an upstream range
contract. In particular:

- A rolling window should include older conversations **updated** in the window,
  not only conversations created in it.
- Determine whether included conversations contain their complete message trees
  or only message fragments. Do not silently treat those shapes as equivalent.
- Requested date ranges, actual source coverage, export creation time, download
  time, capture time, and successful indexing time are different facts.
- A seven-day window downloaded weekly has little margin for delay. A 30-day
  window tolerates more delay but produces much more overlap. Neither implies
  that an arbitrarily long interruption is covered.

## Verified current behavior

### History merging is already conservative

Claude exports are indexed with stable conversation and message identities within
an account namespace. Omitted conversations do not delete prior conversations.
The `user-export` copy-authority rule allows a copy to replace a conversation only
when it contains every stored message ID; otherwise it conservatively adds missing
messages. Do not weaken this while optimizing storage or writes.

The regression test
`TestClaudeImportFullThenOverlappingPartialExports` in
[`claude_import_test.go`](../../internal/archive/claude_import_test.go) exercises:

1. A full baseline with two conversations.
2. A partial export that omits one old conversation, extends another, and adds a
   new conversation.
3. Another export with identical conversation ZIP bytes but a new manifest.
4. A late older partial export.
5. A fresh catalog rebuilt from captures after the managed import collection is
   removed.

It verifies retained history, no duplicate catalog records, the latest reply, and
an offline rebuild. It also demonstrates the storage problem: the byte-identical
ZIPs from separate exports remain as separate retained files.

Other existing tests cover older exports, edited branches, old/new export formats,
multiple Macs, corrupt parts, stale downloads, unknown content blocks, and ZIP
integrity. These are foundations, not proof that arbitrary message-only deltas
already have perfect update, ordering, or branch-selection semantics.

### The storage layers are separate

The current path through the system is approximately:

```text
User's downloaded manifest / ZIPs
    -> managed import collection
       <capture_root>/.claude-imports/<host>/<source>/snapshots/<job-id>/
           job.json
           claude-export.json       # credential-free inventory
           parts/...
    -> preserved source capture
       <capture_root>/<host>/<source>/files/...
    -> derived catalog
```

See the implementation for exact filenames and any additional retained inventories
or reader leases. Distinguish logical references from physical allocation when
measuring copies, hard links, compression, and temporary space.

Current behavior:

- A manifest-backed job ID is a hash of the manifest bytes. A new manifest can
  create a new snapshot even when its conversation payload duplicates an earlier
  export. Loose-file jobs use selected-file identity, size, and modification time.
- Within the same job, verified parts and retries are reused. There is no general
  cross-export payload compaction.
- Downloaded originals are not automatically deleted.
- The managed collection and preserved captures retain payloads separately.
- `claudeWatchKeep = 20` bounds remembered watch activity. It **does not delete
  collected export files or import jobs**.
- `claudeImportBudget` is currently **64 GiB per managed source collection**. It
  stops further collection when exceeded; it is not a cleanup policy and does
  not account for all copies elsewhere on disk.
- Projects, memory, artifacts, and unknown nonexcluded categories may be retained
  as export evidence even though only conversations are currently indexed.

Do not solve this by raising the cap, keeping only the newest ZIP, deleting the
baseline, or calling successful catalog insertion sufficient preservation.

### Catalog deduplication does not avoid the parsing work

`claudeAIAdapter` currently has a whole-source fingerprint and `Discover`; it does
not implement `partialAdapter` / `partialPlanner`.

When the collection changes, discovery enumerates the accumulated inventories,
verifies their inputs, validates conversation records, and then reads conversation
inputs to emit records. Record digests can avoid redundant catalog writes after
that work. They do not avoid earlier I/O, decompression, JSON parsing, or repeated
reconciliation of overlapping conversations.

Compaction should not merely shrink stored bytes while leaving every import to
scan and decode the entire compacted archive. The retained representation and the
incremental parsing boundaries need to be designed together.

### Completeness is not account or date-range coverage

A complete manifest means its required, nonexcluded files were collected. It does
not prove a full-account export or continuous coverage of a requested date range.
A narrow export with its own complete manifest can finish normally. A larger
manifest with missing parts remains incomplete; available conversations can be
indexed manually, while the Downloads watcher waits before declaring the export
successfully imported.

The current manifest/inventory model does not track authoritative rolling-window
coverage or a last successfully covered cutoff. Preserve unknown coverage as
unknown. Receipt cleanup must not turn unproven coverage into a success claim.

## Code map

| Area | Start here |
|---|---|
| Collection, jobs, part verification, copies, budgets | [`claude_import.go`](../../internal/archive/claude_import.go): `collectClaudeExport`, `collectClaudePart`, `verifyClaudeStoredPart`, `publishClaudeInventory`, `claudeImportRetainedBytes`, `claudeImportLock` |
| Sanitized inventories, input enumeration and ordering, ZIP validation | [`claude_export.go`](../../internal/archive/claude_export.go): `claudeExportInventory`, `claudeExportInput`, `claudeExportInputs`, `claudeInventories`, `verifyClaudeExportInput`, `verifyClaudeZIP` |
| Parsing, identity and selected branches | [`adapter_claude_ai.go`](../../internal/archive/adapter_claude_ai.go): `claudeAIAdapter`, `Discover`, `readExport`, `record`, `claudeAIExtractor` |
| Copy authority and conservative merging | [`copies.go`](../../internal/archive/copies.go): `resolveAuthority`, `holdsEvery`, `mergeConversation` |
| Record digests, writes and message retention | [`ingest.go`](../../internal/archive/ingest.go): `ingestPass`, `ingestRecordContext`, `pruneMessages`, `keepStoredMessages` |
| Existing incremental parser/planner contracts | [`ingest_parts.go`](../../internal/archive/ingest_parts.go), [`ingest_plan.go`](../../internal/archive/ingest_plan.go): `partialAdapter`, `sourcePart`, `partTracker`, `partialPlanner`, `sourceWorkPlan` |
| Capture input selection and publication | [`capture_plan.go`](../../internal/archive/capture_plan.go): `(*capturePlan).claudeAI`; [`capture.go`](../../internal/archive/capture.go) |
| Retained-input inventories and evidence identity | [`capture_acquire.go`](../../internal/archive/capture_acquire.go): `freezeManifestInputPolicy`, `retainedRecordIdentity` |
| Pins, live readers, history and reclamation boundaries | [`capture_pins.go`](../../internal/archive/capture_pins.go), [`capture_history.go`](../../internal/archive/capture_history.go), [`capture_history_verify.go`](../../internal/archive/capture_history_verify.go), [`capture_fs.go`](../../internal/archive/capture_fs.go) |
| Delivery identities and durable handoff | [`capture_delivery.go`](../../internal/archive/capture_delivery.go) |
| Downloads watch, batches, completion and remembered activity | [`claude_downloads.go`](../../internal/archive/claude_downloads.go): `Check`, `advance`, `claudePruneFetches`; [`claude_import_api.go`](../../internal/archive/claude_import_api.go) |
| Existing capture/index coordination | [`background_run.go`](../../internal/archive/background_run.go), [`capture_index_api.go`](../../internal/archive/capture_index_api.go), [`incremental_sync.go`](../../internal/archive/incremental_sync.go) |
| Import UI and status fixtures | [`assets/onboarding.js`](../../internal/archive/assets/onboarding.js), [`claude_import_test.go`](../../internal/archive/claude_import_test.go), [`claude_downloads_test.go`](../../internal/archive/claude_downloads_test.go), [`web/tests/browser/fixtures.mjs`](../../web/tests/browser/fixtures.mjs) |

Read [capture history](../capture-history.md), [capture compression](../capture-compression.md),
[SQLite record preservation](../sqlite-records.md), [incremental sync](../incremental-sync.md),
and the Claude import section of [configuration](../configuration.md). Reuse their
contracts where appropriate; this handoff does not imply that an existing SQLite
format can be used unchanged for JSON exports.

## Design questions to resolve together

### 1. What is the reusable unit of retained source data?

Compare archive-, ZIP-member-, conversation-, message/revision-, and chunk-level
storage. Whole-archive hashing is a useful first layer but insufficient alone:
rolling exports can change archive hashes while repeating almost all chat data.
Conversely, retaining a complete conversation copy on every small extension can
still repeatedly store a very large thread.

Consider immutable content-addressed payloads plus small export-membership and
provenance receipts. Treat that as a candidate, not a mandated format. Explain:

- Which bytes/fields are authoritative source evidence and which are derived.
- Whether preservation is byte-exact, source-faithful under a documented
  transformation, or only catalog-derived. Do not interchange those claims.
- How unknown conversation fields, unknown blocks, branches, attachments,
  tool payloads, and nonconversation categories survive future parser upgrades.
- How identical observations share payloads while genuine edits, revisions,
  metadata changes, and differing branch selections remain representable.
- How logical account/organization/conversation identity remains separate from
  physical blob identity. Equal text or equal hashes do not merge conversations.
- How original paths, export membership, hashes, source times, and importer/parser
  versions remain available without copying large payloads into every receipt.
  Existing evidence locators, source IDs, links, and persisted identity encodings
  must keep resolving after migration.

Distinct history can legitimately grow. The goal is to eliminate redundant payload
multiplication, not promise constant space for ever-growing conversations or
silently discard genuine source revisions. Specify a separate policy for any
bounded receipt/metadata history.

### 2. How does the representation make imports incremental?

Aim for expensive work proportional to newly received bytes and newly introduced
or changed record versions, plus bounded/indexed metadata lookups. An unchanged
or already applied export should not require decoding historical conversations.

Decide how to:

- Verify a new download once, extract reusable source units, and publish their
  references durably without repeatedly rescanning prior snapshots.
- Identify unchanged record content even when ZIP order, filenames, shard
  boundaries, or transport metadata differ.
- Preserve legitimate source changes without unstable hashes of incidental local
  paths, collection times, or arbitrary map iteration.
- Integrate with `partialAdapter`, planning, `source_item_states`, record digests,
  cancellation, progress and the existing ingest coordinator.
- Track **preserved**, **indexed**, **current for this parser**, and **eligible for
  reclamation** separately. A write-skipping digest is not a preservation proof.
- Resume after interruption and handle concurrent imports without duplicate
  acquisition or racing independent writers.
- Invalidate the right work on parser upgrades, force reindexing, evidence repair,
  and catalog loss. A warm fast path must not hide corruption or certify missing
  source evidence.

Periodic integrity verification or garbage collection may need broader scans.
Keep their cost explicit and off the ordinary small-import critical path; do not
achieve speed by abandoning verification.

### 3. What can be compacted, and when is deletion safe?

Specify one ownership/reference model across Downloads, managed collection,
preserved captures, immutable input inventories, readers, pins, backups and
pending work. List every root that protects a payload from reclamation.

At minimum:

- Publish and verify a durable replacement and all required references before
  retiring the representation it replaces.
- An empty fresh catalog must be rebuildable from retained source evidence after
  the original downloads and redundant import snapshots are gone.
- Do not use the live catalog as the only surviving copy of source data.
- Preserve active reader leases, durable pins, unindexed inputs, incomplete jobs,
  rollback requirements and other hosts' references.
- Define behavior for interrupted writes, unplugged/read-only volumes, unknown
  liveness, corrupt blobs, missing references, mixed software versions and
  failures during deletion. Missing proof keeps data protected.
- Account for peak migration/compaction space, scratch bytes, reserve margins and
  temporary copies, not just final compressed size. Compression alone is not
  deduplication.
- Download cleanup needs explicit ownership and a documented user-facing policy.
  Never delete unrelated files by basename, glob, age, or a guessed match. Handle
  files changed since verification and shared references to a downloaded file.
- Carry forward the existing privacy boundary: account/sign-in files and the
  `light_metadata` category remain excluded. Signed download URLs and original
  manifests with download capabilities stay in host-private support storage,
  outside the portable capture/object store. Filter before publishing retained
  objects; preservation claims must describe the filtered representation.
- Do not modify Claude Desktop's data. Do not run destructive trials on the real
  library or the user's Downloads while developing this feature.

Avoid unsafe intermediate formats that let an older writer prune new evidence.
If a new format requires a version bump or older-writer refusal, include that in
migration and rollout. Migration must be idempotent, resumable, and initially
reviewable without destructive cleanup.

### 4. How does the weekly batch remain understandable?

Separate source coverage from the arrival schedule and from file completeness.
Retain source-provided range/scope/cutoff information when available; preserve
unknowns when it is absent. Derive no full-history guarantee merely from daily
requests, a weekly download habit, or a complete manifest.

Do not discard an older overlapping export merely because a newer export's date
range contains it. Earlier exports can contain genuine revisions, branches, or
other evidence absent from a later export. Prove equivalence or preserve the
additional evidence under the chosen history contract.

Coverage/gap detection and automated export creation are adjacent work, not a
requirement to build a new scheduler here. However, compaction receipts and
checkpoints must leave those future capabilities possible. A checkpoint for a
successfully imported export should not confuse its source cutoff with the day
the user finally downloaded it.

The small `claude-ai-settings` companion source is separate. Its selected model,
effort and thinking observations are not execution usage and are not part of a
chat export's date-window coverage. Do not collapse that provenance into per-message
model assertions or delete its observations while reclaiming export ZIPs.

## Acceptance scenarios and measurement

Use small synthetic fixtures and instrument actual work. Prefer byte/read/parse
counts and stable identities over brittle wall-clock thresholds. Do not use real
conversation bodies or credentials in committed fixtures.

| Scenario | Required result |
|---|---|
| Partial-only onboarding, followed later by an optional full export | Useful partial history, explicit unknown coverage, and a correct merge when broader history arrives. |
| Full baseline followed by 7-day and 30-day subsets | Omitted old conversations remain; changed and new conversations join the same account's history. |
| Daily requests, latest export downloaded weekly | Correct import without requiring every intermediate daily export to arrive. Missing coverage remains explicit where it cannot be proven. |
| Weekly batch of overlapping daily exports | Import each genuinely new observation; do not reread the baseline and every prior batch for each arrival. |
| Identical ZIP repeated under a new manifest | No duplicate conversation/message rows and no additional retained large payload after compaction reaches a settled state. Small provenance receipts may remain. |
| Same content repackaged or reshuffled across shards | Reuse the chosen source units; archive-byte differences alone must not force lifetime payload duplication. |
| Same conversation repeatedly extended | Preserve new material and valid history without retaining another complete giant thread copy for every small extension, unless measurements justify a documented alternative. |
| Old conversation resumed inside the window | Its new activity is incorporated even though its creation time predates the window. |
| Older exports arriving after newer ones | Preserve history and source ordering; no regression to stale content/branch state. |
| Edited/regenerated branches or fragmentary conversation records | Honor the documented merge/coverage contract; no inferred deletion from an omission and no false claim that a fragment is complete. |
| Interrupted or corrupt multipart download | Resume verified work; keep incomplete distinct from complete; never acknowledge or reclaim an unverified dependency. |
| Repeated unchanged import after restart | Avoid historical payload parsing and redundant writes, including after the process loses in-memory caches. |
| Parser upgrade, force reindex, or catalog loss | Replay from retained source data works under the recorded compatibility/equivalence contract. |
| Compaction interrupted at each publication/reclamation boundary | Previously accepted evidence remains readable; retry is idempotent; no dangling committed references. |
| Another Mac, active reader, backup or pin references old data | Reclamation respects every supported reference and lifetime boundary. |
| User-managed Downloads and files changed since verification | No collateral deletion; unresolved ownership or identity prevents automatic cleanup. |
| Existing legacy collections migrated | Same searchable history and promised source evidence, with a resumable path and no mandatory new full export. |

Measure before/after for both 7-day and 30-day windows over several weekly batches:

- Logical export bytes versus physically retained unique payload bytes, by layer.
- Import receipt/index metadata size separately from conversation/asset payloads.
- Peak temporary space and migration/compaction space requirements.
- Historical bytes reopened, hashed, decompressed and parsed per new import.
- Records parsed versus catalog records inserted, changed, or skipped.
- First import, warm repeat, restart repeat, small update, overlapping batch,
  compaction and offline-rebuild behavior.

For a fixed set of unchanged source observations repeatedly re-exported, large
payload storage should stop growing once they are represented, and prior payload
parse work should not scale with the number of historical export receipts. State
any remaining metadata growth and scheduled maintenance costs explicitly.

## Suggested phases and completion criteria

A reasonable order to evaluate, rather than a required implementation layout:

1. Measure the current pipeline and settle the preservation/equivalence and
   ownership contracts. Produce the unified design and migration plan.
2. Add reusable, versioned retained source units and durable export references
   without deleting legacy inputs. Prove offline replay and failure recovery.
3. Make the import adapter plan and parse those units incrementally; measure
   restart-safe savings and parser-upgrade behavior.
4. Add resumable migration, reclamation planning, and cleanup under the same
   reference model. Enable deletion only when replacement evidence is proven.
5. Expose the minimal progress, storage accounting and cleanup controls needed for
   users to understand the lifecycle. Reuse existing job/source controls.

The result should include a documented format and reference lifecycle, measured
storage and import-work reductions under the expected cadence, compatibility and
migration behavior, and data-safety tests. Scope implementation into follow-up PRs;
leave the preceding import/settings PR focused on its existing behavior and this
handoff.

Follow `AGENTS.md` for setup, Node version, architecture boundaries and validation.
Useful starting checks include:

```sh
go test -vet=off -run '^TestClaudeImportFullThenOverlappingPartialExports$' ./internal/archive
go test -vet=off -run 'TestClaudeImport|TestClaudeAI|TestClaudeDownloads' ./internal/archive
go test ./internal/architecture
go test -short ./...
```

Extend the existing test files for relevant behaviors. Keep ordinary tests cheap,
retain slow data-safety cases, and use the repo's opt-in probe/benchmark conventions
for larger measurements. Do not build frontend bundles concurrently with Go
compilation. UI changes need the normal control audit and relevant browser checks;
new source formats/parser behavior need the appropriate versioning and conformance
fixtures. Query Table patches, unrelated platform expansion, and a new Claude
export-creation automation are outside this handoff.
