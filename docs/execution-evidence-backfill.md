# Execution evidence: migration and backfill manifest

Status: implemented and independently reviewed, 2026-11-07. The original feature passed Go fast/full suites and six targeted race cases after the acknowledged-launch cycle correction and resolver-coupled cache upgrades. The exact cycle reproducer failed before the fix and passed unchanged afterward. Those checks, earlier reproducers and library-scale measurements describe the feature before the subsequent master integration; see the design document for integration validation.
Companions: [design](execution-resolver-design.md), [audit](execution-identity-audit.md).

Ingestion now records typed execution evidence before it reconciles or
filters what a provider held. This page lists what that evidence is, what an
existing catalog can recover from the rows it already retains, what needs a
provider read, and what stays unknown. Nothing here rewrites transcripts,
parents or usage; the stored graph remains as earlier versions wrote it, and
resolution decides how far to trust it.

## Upgrade effects

These changes rebuild derived data through the ordinary maintenance paths;
they do not rewrite source transcripts or discard reported usage.

| Change | Existing-catalog effect |
| --- | --- |
| Resolver `execution-v8` | Supersedes earlier labels/events execution digests once, including Workflow run parentage and conversation names. Attribution and membership rebuild; inference can be reused when its complete inputs still match. Pending results remain visibly qualified. |
| Request ledger `tools-v13-request-sources` | Adds first/last usage-message links and rebuilds request rows from stored messages. Request identities and reconciled accounting are preserved. |
| Parallelism stored version `4/execution-v8` | Re-derives cached activity with the shared resolver occurrence index, including copied launches. Source-generation changes trigger a fingerprint sweep; a resolver-version change forces the existing one-time rebuild even when source data is unchanged. Corrected native-ID, locator and ancestry triggers are installed on existing catalogs too. |
| Origins stored version `2/execution-v8`, execution-generation/fingerprint metadata | Recomputes origins once on a resolver upgrade, including when source data is unchanged; subsequent opens stay idle and source changes republish only when shared lineage changes. |
| Retained evidence inputs corrected or removed | Marks reconstructed evidence stale, withholds unsupported group claims and weakens materialized inference copies to hints. Ordinary backfill repairs the current claims; independent provider assertions are preserved. |

Parallelism shares the resolver's complete occurrence index for activity and
fingerprints. Routine sweeps use the source-change journal and occurrence scope,
plus changes to resolved copied launches, to compare affected stored rows. If
the earlier snapshot is unavailable or the scope cannot be proved, they compare
all stored fingerprints. Finding the scope still walks the compact in-memory
graph; this follow-up was not benchmarked on a full library. Publications older
than the swept generation stay pending and are derived again.

Launch-copy inference requires proven one-way ancestry. Two launches on cyclic
ancestry that identify the same child keep their execution bindings and issuing
parent evidence, leaving the child's parent ambiguous. A bounded reverse search
that cannot finish also leaves the observations intact. Resolver `execution-v5`
supersedes the earlier rule that missed this fallback path. Both stored consumer
version keys include the resolver version automatically, rather than relying on
an unrelated source write to repair old cached projections.

A bounded or interrupted first backfill does not mark unprocessed members
complete. Group-derived claims are withheld while any member is stale. A stored
Claude launch/parent link cannot independently preserve derived parentage after
the current group evidence withdraws its support. Launch ownership, family
membership and source hints remain inspectable.

No maintenance or migration was run against the live user library during this
implementation. Provider rereads required by the manifest remain separate from
reconstruction using retained rows. Unproven Codex `token_count` copies remain
unresolved/partial, including their qualified request counts and spans.

## What is stored

`execution_claims` (one row per claim, `WITHOUT ROWID`, keyed by conversation
and a stable claim key) and `execution_evidence_state` (one row per
conversation: extractor version, `input` = `ingest` or `retained`, and the
message ID policy). Both are created by `schema.sql` when a catalog opens; no
data migration runs then.

| Column | Meaning |
| --- | --- |
| `source` | the writer that owns the row: `record` (derived from the conversation's record or rows), `adapter` (retained by the adapter before filtering), `claude-group` (Claude group lineage). A writer replaces only its own rows. |
| `kind` | `parent`, `launch_outcome`, `workflow_launch`, `history_copy`, `copy_reconstruction`, `suppressed_record` |
| `subject` | relative to the owning conversation: `conversation`, `launch:<call id>`, `composer:<id>` |
| `object` | a lookup key: `native:<provider>\|<account>\|<native id>`, `claude-agent:<account>\|<root>\|<agent id>`, `claude-workflow:<account>\|<root>\|<run id>` (all immediate members of a run), `task-path:<path>` (scoped to the owner's children), or empty |
| `origin` | how the claim was extracted: `native` (a field the provider recorded), `derived` (a named parser rule read it), `legacy` (stored, provenance unknown), `synthesized` (a fallback an extractor chose; a hint, never proof) |
| `authority` | what the claim establishes, apart from its origin: `verified` (the provider's record, or an envelope that verifies the outcome: a structured handle or refusal, a Claude launch acknowledgement), `corroborated` (a derived reading another observation confirms, such as a text acknowledgement whose assignment matched), `inferred`, `unverified` (legacy), `fallback` (synthesized). A verified acknowledgement parsed by a rule is `derived`/`verified`, so contradictory continuation metadata does not outrank it: resolution reports an identity conflict. |
| `rule` | rule and version, for example `claude-launch-ack/v1`, `claude-root-fallback/v1`, `copilot-session-fallback/v1`, `copilot-retained-session-parent/v1`, `launch-envelope/v1`, `codex-session-meta/v1`, `cursor-fork-length-at-spawn/v1`, `cursor-echo-subagent/v1` |
| `namespace`, `id_kind`, `locator`, `detail_json` | identifier namespace (`provider/account`), identifier kind (`native-event`, `call-id`, `locator`), the supporting observation, and rule-specific detail (outcome and handle, fallback reason, copy boundary and mapping, copied calls) |

`meta.execution_evidence_generation` advances on every claim change. Extractor
version: `execution-evidence-v5`. v2 recorded authorities and marked Codex
`forked_from` history; v3 records provider parent fallbacks (Copilot) and
bounded Cursor copy regions; v4 recognizes validated harness usage footers on
completed `Agent`/`Task` delegation results. That footer establishes the named
child execution, including when its transcript is absent. Resolver
`execution-v6` applies the same recognition to retained rows and supersedes
stored consumer projections. Scoped reported usage remains evidence rather than
additional aggregate usage. Each is derivable from retained rows only as far
as the manifest says. A provider read from an earlier version still counts as
complete (`input='ingest'`), except for Copilot, VS Code Copilot and Cursor:
their v3 evidence needs the adapter. v5 adds `workflow_launch` claims, recovered
from retained Workflow calls and their structured results. Resolver
`execution-v7` matches the account, root session ID and run ID in each child's
native transcript identity. A run can have several children: each keeps its own
execution and accounting while gaining the launching conversation as parent.
The result's `transcriptDir` must agree with its `runId` and root session, and
its status/type must identify a successfully launched local workflow. Report
prose and printed journal entries are not acknowledgements. Competing callers
remain ambiguous. Retained-row resolution uses the same rule before backfill;
later children and corrected or removed acknowledgements invalidate dependent
views through the run's lookup key. This does not rewrite stored parent fields.

Origin and authority stay distinct. A claim's strength is its origin capped
by its authority: `inferred` caps it at derived, `unverified` at legacy and
`fallback` at synthesized. So a `native`/`fallback` claim is a hint, never
proof (`storedExecutionClaim.strength`). A claim
whose object names a transcript not yet indexed is kept; it is a dependency
that resolution and invalidation notice when the transcript arrives.

Message identifier kinds are not a new column. `messageIDKind(policy,
native_id, evidence_locator)` classifies a stored message: the Codex and TL1
extractors write `line-N` only for events without an ID, and write the same
line into the evidence locator. Policies: `line-locator/v1` (Codex, TL1),
`provider-event/v1` (Claude, Cursor), `unclassified/v1` (others: only a
corroborated `line-N` is classified). Without a state row the kind is unknown
and occurrences match on observation only.

## Manifest

| Provider / condition | Evidence | Recovery for an existing catalog |
| --- | --- | --- |
| Claude group lineage (accepted acknowledgement versus root fallback) | `claude-group` parent claims: derived `claude-launch-ack/v1` or `claude-async-text-ack+assignment/v1` with the launch call, or synthesized `claude-root-fallback/v1` with `reason` (`no-acknowledgement`, `duplicate-child`, `competing-acknowledgements`, `cycle`) | **Retained rows.** Lineage re-runs over stored delegation messages and first assignments (`storedClaudeGroup`). Stored `parent_id`/depth are not changed. A group whose root or delegation results were not retained keeps only legacy parents, which resolution treats as unverified. The links lineage materialized are copies of the same inference: a launch's stored child (`claude-accepted-launch-link`) and a non-root stored parent (`claude-accepted-lineage`). Once the child has group evidence, they stay derived only while it accepts the same launch or parent. Otherwise, after a fallback re-derivation or while the group is stale, they read as synthesized `…-unsupported` hints, so the parent is not resolved from them alone (`gradeClaudeMaterializedLinks`). A provider read's acknowledgement keeps them derived when its result is later pruned. |
| Launch outcomes (all providers) | `launch_outcome` per launch call: `accepted` (structured handle, task path, or the Claude async text acknowledgement), `rejected` (envelope error or harness refusal), `unknown`. A validated harness usage footer on a completed `Agent`/`Task` delegation result also establishes creation (`harness-usage-footer/v1`). Report prose is never an acknowledgement. Conflicting readings are all kept. | **Retained rows.** Re-derived from stored `delegation`/`delegation_result` (and Codex `tool_result`) messages. Lets global resolution stop scanning result bodies. |
| Copilot CLI and VS Code Copilot session-root parents | When a subagent's start event matches no tool call, or there is no start event, the adapter falls back to the session as parent. It records that as `adapter` `parent`, `synthesized`/`fallback`, `copilot-session-fallback/v1`, `detail.reason` (`unmatched-tool-call`, `no-start-event`, `no-start-call`), and the record omits the native parent-field claim. A matched start stays native. | **Provider read.** Extractors `copilot-v3-parent-fallback` and `vscode-copilot-v3-parent-fallback` make the next index reread every session. Until then, a backfill of an earlier read cannot tell a fallback from a matched start. It records any depth > 0 parent that equals the session (the workspace source) as `legacy`/`unverified`, `copilot-retained-session-parent/v1`: a hint, as historical unknowns stay. |
| Cursor and Cursor CLI parents; Conductor | Cursor's parent is the composer's own `subagentInfo.parentComposerId`, kept even when that composer was deleted. Depth follows the spawn chain, at least 1. Cursor CLI reads `parentAgentId` at depth 1. Conductor sets only message-level parents and no conversation parent. | No fallback parent exists in these adapters (inventory 2026-11-07), so nothing changes. |
| Provider parent field (non-Claude, or a Claude conversation whose export names another provider's parent) | `parent` claim, native, `provider-parent-field/v1`, with `agent_depth` and `agent_path`; its object is in the parent's namespace (`ParentProvider`/`ParentAccount` when recorded, else the child's) | **Retained rows**, from `conversation_parent_refs` (the raw provider field apart from `parent_id`, keyed by the child, without the parent's namespace). The namespace comes from the parent's origin observation (`parent_provider`, `parent_account`), or is the child's own when an adapter of the child's provider recorded it. Otherwise it was never recorded: the claim becomes `legacy`/`unverified`, `retained-parent-ref/v1`, `detail.parent_namespace = "unrecorded"`. Claude's own parent field is excluded: it is post-reconciliation. |
| Codex continuation and fork history (`session_meta` note) | `history_copy`, native, `codex-session-meta/v1`: `forked_from_ordinal_exclusive`, `subagent_history_start_ordinal`, `history_base`, `forked_from`; mapping `ordinal` or `unmapped`. A rollout forked from another thread without a delegating parent field is a continuation (`codex-forked-from/v1`) of it. | **Retained rows, from Codex index `message-model-v19` on.** The note is a stored metadata message, but extractors before `message-model-v19-codex-continuations` did not store it. A catalog indexed earlier (the 2026-11-07 backup copy reads `message-model-v17`) has no notes, so backfill writes no `history_copy`. The next index of such a source rereads it. Until then, core reports those children `retained-history-undeclared` (status lists them under `provider_read`). Copies match originals only by provider event ID; `line-N` copies become copy candidates (unresolved). When the original is not retained, the observations the ordinal covers (every observation when unmapped) are copy candidates of nothing, with `Context = missing:<key>`. A candidate stays a counted observation of the transcript that holds it, flagged unresolved, so that transcript's self coverage reads partial. It is never another execution's activity. |
| Codex/TL1 `line-N` identifiers | ID policy `line-locator/v1` | **Retained rows.** Recoverable from `native_id` and `evidence_locator`; no reread. |
| Cursor fork copies of the parent's first messages | `adapter` `history_copy`, `cursor-fork-length-at-spawn/v1`: the boundary and a `mapping`. `shared-native-ids` means the copied bubble IDs matched the parent's headers, so the copies were left out. `unverified` comes with the exact copied bubble IDs (`detail.ids`, v3). `unmapped` means the reported length exceeds the bubbles known (`known`), and comes with the IDs it may cover (`detail.suspected`). Also recorded: first and last copied IDs, copied tool calls, and whether the parent was read or deleted. | **Provider read.** The copies were dropped before insertion. The Cursor extractor is `cursor-v3-bounded-copies`, so the next index of a Cursor source rereads every composer. Occurrences (`cursorCopyRegion`):<br>- a listed copy carrying the parent's ID is inherited;<br>- a diverged ID is a copy candidate (`cursor-copy-unmatched/v1`);<br>- a suspected copy is a candidate (`cursor-copy-unplaced/v1`).<br>A candidate stays a counted observation of the fork, flagged unresolved, so the fork's self coverage reads partial; it is never borrowed into another execution. Copied tool calls follow their call message. A v2 claim without IDs is placed by its length and `last_id`. Its region is exact only when both ends sit where the length puts them; otherwise it is suspected. A backfill from retained rows records `input='retained'` and reports `cursor-copy-evidence` missing. An unverified copy whose parent is not retained is an inherited copy of a missing original (`cursor-fork-missing-original/v1`, zero object): the copy is its navigation target, and no owner is borrowed. |
| Cursor echo subagents (copies of a subagent its fork re-created) | `adapter` `suppressed_record`, `cursor-echo-subagent/v1`, on the subagent copied, with the echo's composer, spawn call and reason | **Provider read**, as above. |
| Cursor deleted parent rebuilt from a fork's copy | `adapter` `copy_reconstruction`, `cursor-deleted-parent-copy/v1` | **Provider read**, as above. |
| Original evidence unavailable (source deleted, partial capture) | none | **Unknown.** Existing rows are preserved; the conversation's relationships stay legacy (unverified), coverage reports `typed-execution-evidence` missing, and nothing is reconstructed from the old graph. |

## Running the backfill (`pharos execution-evidence`)

Retained-row backfill and provider reindex are different operations:

- **Retained-row backfill** (this section) re-derives typed evidence from the
  rows a catalog already holds. It reads only the catalog, never a provider,
  and never changes transcripts, stored parents, usage or accounting.
- **Provider reindex** reads the sources again. It is what an extractor
  version change triggers (`pharos index`, or the next background index),
  and it is the only way to recover evidence an adapter dropped before
  insertion. Required versions:
  - Cursor `cursor-v3-bounded-copies` (copies, echoes, reconstructed parents);
  - Copilot `copilot-v3-parent-fallback` and VS Code Copilot
    `vscode-copilot-v3-parent-fallback` (parent fallbacks);
  - Codex `message-model-v19-codex-continuations`, only for rollouts indexed
    before it (session notes).

  Claude, TL1 and the other providers need no reread: their evidence is in
  retained rows.

The supported command, run explicitly by the owner of the library:

```sh
pharos execution-evidence status                 # read only: pending scope, provider reads, unavailable provenance
pharos execution-evidence run --limit 5000       # derive at most 5000 conversations, in batches of at most 500
pharos execution-evidence run                    # continue until nothing is pending
```

`status` reads one read-only snapshot, so its counts agree with each other,
and writes nothing (tested: the catalog fingerprint, including `meta` and
the dirty marks, is unchanged). Its JSON fields:

- `version` and `resolver`;
- `conversations`;
- `by_provider`: conversations per state (`absent`, `other-version`, `ingest`,
  `retained`);
- `pending`: absent or other-version;
- `retained`;
- `provider_read`: per provider (Cursor, Copilot, VS Code Copilot, Codex),
  the extractor, the conversations not read at the current version, what a
  read recovers, and how it happens;
- `unavailable`: what stays so after any run:
  - `parent_namespace_unrecorded`;
  - `launch_outcome_unknown`;
  - `history_copy_unmapped`;
  - `parent_fallbacks`: parents no record names, counted by rule
    (`claude-root-fallback/v1`, `copilot-session-fallback/v1`,
    `copilot-retained-session-parent/v1`, `retained-parent-ref/v1`, …);
- `dependents_unmarked_generations`: source changes not yet marked on labels
  and agent events;
- `next`: present only while some conversations are pending.

`run` processes conversations whose state is absent or of another version.
It runs in committed batches of at most `--batch` (default 500) whose writer
hold stays near one second (`executionBackfillBudget`): a batch that took
longer halves the next one, and a quick one doubles it back to `--batch`.
Each batch marks the labels and agent events whose evidence it changed as
pending, in the same transaction. `run` stops after `--limit` (0: all) and
prints `{"derived": N, "status": …}`. It is resumable, because a later run
continues with what is still pending, and idempotent: a run with nothing
pending writes nothing and leaves the evidence generation alone. It runs as
the leased operation `catalog.execution-evidence` (Affects `an.labels,
an.agent_events`; stopping between batches keeps what committed).

Opening a catalog, ingestion, the background chain and requests never call
it, and this change did not run it against any library. Until it runs,
resolution uses legacy rows for those conversations: the pre-existing
behaviour with uncertainty made explicit. A provider reread writes
`input='ingest'` evidence and supersedes it.

Re-running the backfill or re-reading the same source is idempotent: claim
keys are stable, unchanged rows are not rewritten, and the evidence
generation does not advance.

**Retained evidence follows its rows.** Evidence derived from stored rows
(`input='retained'`) is only as current as those rows. Triggers
(`retainedEvidenceTriggers`, recreated on every open) mark it stale when a
writer other than ingestion inserts, corrects or deletes a row the backfill
reads:
- a launch, a result, a Codex session note or a first assignment;
- an origin observation of a parent's namespace;
- a workspace's source.

The state's version becomes `stale/<version>`, which bumps the source
generation. It also marks every retained member of a Claude root group,
since group lineage reads each member's messages.

Until `run` re-derives it, the conversation reads as pending, like any
other version:
- resolution and the legacy launch inputs read the stored rows;
- the group's `claude-group` claims and the conversation's `history_copy`
  claims are not read;
- labels and agent events built on the old attribution are marked as
  pending through the dependency digests;
- `status` lists the conversation under `other-version`.

Ingestion writes its messages before it records its state, so its own
writes leave the evidence current. Evidence from a provider read
(`input='ingest'`) is not marked: it is what the provider asserted when it
was read, and a later pruning or correction of the transcript does not
withdraw it. `conversation_parent_refs` holds only the provider's field,
which only ingestion writes.

A stale member withdraws its whole group's `claude-group` claims, read
for every member, including those with no state row yet. That case arises
between the batches of a first backfill: the batch holding one member
writes the group's claims for every member, but a state only for the
members it processed. No placeholder state is written for the others, and
the backfill still processes them. The next batch that reaches any member
re-runs the group's lineage and makes its claims readable again.

## Durability across copies, merges and recovery

- **Host copies.** The writing host replaces its claims; a host that only
  merges messages adds its adapter's claims and re-derives the rest from what
  is stored, never retracting the writer's.
- **Claude group unions** (`copies.go`) and usage attribution repairs run
  `linkClaudeGroupDelegations`, which rewrites the group's lineage claims.
- **Catalog history recovery** exports `execution_claims` and
  `execution_evidence_state` with the workspace's other observations. After
  the production writer it does three things:
  - restores only the `adapter` claims (they cannot be re-derived from rows)
    and the exported state;
  - re-derives the `record` and lineage claims from the restored rows, so an
    obsolete record claim is not resurrected;
  - marks dependents.

  The state stays `input='ingest'` only when the recovered read is complete
  for this version. An older-version or state-less export becomes
  `retained`, and the evidence it cannot supply is reported missing.
- **Library transfer** (move or copy between libraries) carries
  `execution_claims` and `execution_evidence_state` as history: before this
  change the snapshot's sanitization dropped them. An accepted (newer) copy's
  claims replace the destination's for that conversation; a divergent union
  keeps both sides' claims. The source re-indexes its captures before the
  snapshot, so an adapter's claims are current when they travel. Model
  request rows travel too, as accepted ledger rows (not re-derived). Their
  usage links (`source_message_id`, `final_message_id`) are catalog-local
  message IDs, mapped to the destination's IDs for the same native messages
  like every other reference. A link the source never recorded (an older
  ledger or schema) stays NULL. In a transcript holding copied history, the
  request then reads as unlinked and unresolved, never as the transcript's
  own. Skill-usage evidence and body message links are mapped by the same
  native-message identity, including preservation of a NULL body link. Tests
  compare source and destination native targets in both ordinary and divergent
  local-ID transfers, with request roles and accounting unchanged.
- **Backups, adoption and offline labeling snapshots** copy the catalog file,
  so both tables travel with it. Labeling prepare records
  `execution_evidence` (versions, generation, coverage counts) in its manifest
  and adds a coverage line when a snapshot predates typed evidence or has
  conversations without it.

## Persisted derivatives

Labels and agent events compute an execution dependency digest per
conversation (`execution_dependencies.go`): a hash of every evidence row its
resolution can read, across its evidence component, including keys no
conversation answers yet. Besides typed claims and evidence state, it covers
what the legacy readings read: the conversation's identity, parent, depth,
path, nickname, start and aliases; every session's identity, links, kind,
start, usage presence and observed flag, and its launch message's time,
position, role, kind, call and body hash; identity links and command
launches; for transcripts without current typed evidence, each launch
result's body hash and the handle it names (so a correction, a deletion, or
the named transcript arriving elsewhere changes the digest of the child it
attributes); and retained origin parents in their recorded namespace.

It also covers the conversation's **occurrence facts**, which labels and
agent events read to decide which of its messages are its own (core's
`occurrenceDependencyLines`):
- for each observation the occurrence index classifies, its kind, the
  occurrence it copies or duplicates, and the stream that recorded that
  original;
- for each model request of a transcript with classified messages, the role
  its usage messages give it (`requestOccurrenceClaims`):
  - inherited, from the original's request, when the messages of its first
    and last usage events (`model_requests.source_message_id`,
    `final_message_id`, written by the tool ledger since
    `tools-v13-request-sources`) are copies. Joined anonymous events keep
    the largest output's counts, which may come from an event between them;
  - unresolved when they straddle copied history, when an event's message
    is itself unresolved, or when the link is missing;
  - request IDs are never matched.
  - **Known limit:** Codex stores `token_count` events as `line-N`
    metadata messages, which are not provider event IDs. A fork's copy of
    one is unproven, so its request stays unresolved: it still counts
    toward the fork's request totals and places a point on its span, with
    coverage partial. Better results need message-level proof of `line-N`
    copies, not request matching;
- the transcript's partial coverage reason, for example
  `retained-history-undeclared/v1` or
  `declared-copy-source-missing/v1`.

Owners are left out, since which execution a stream binds to is already in
the evidence digest. The facts depend on the copy's ancestors' event IDs and
memberships. So a parent's original arriving or being deleted, or a
correction of its event ID, changes the copy's digest. An unrelated append
to the parent (an event ID no copy shares) leaves it equal. The digest's
version lines include `occurrence-dependency/v4-undeclared-history`, as they include the
resolver and evidence versions.

The digest is part of each ledger's structure fingerprint, so a late
acknowledgement in another conversation rebuilds the affected component on
the next background run; stored inferences with unchanged input hashes are
reused. At publication each ledger recomputes the digest inside its write
transaction and discards a build whose evidence changed, leaving a
persistent dirty mark (`publishExecutionDependents`). Both ledgers store the
digest they published (`message_labels_conversations.execution_digest`,
labels schema 5; `agent_events_conversations.execution_digest`, agent events
ledger 4), so a read can tell rows built against superseded evidence before
any rebuild. Publication writes no evidence, so a run after publication
rebuilds nothing.

**Component members.** The same transaction records the members of every
component it published at, keyed by digest
(`execution_dependency_members`), including members with no derivative of
their own. A change that takes a conversation out of a component (its
deletion, a removed or retargeted claim or parent, an identity correction)
is logged under that conversation only, and its former component can no
longer be walked to from it; the recorded members find it. Members of a
digest no derivative rests on any more are pruned at publication.

**One freshness result.** A derivative is pending when it is dirty, or when
the next mark would mark it. `executionUnmarkedDependents` decides that for
the mark and for every reader alike:
- a derivative built without a digest (`''`, an older executable's output)
  is pending;
- with no propagation cursor (`meta.execution_dependents_through`: a catalog
  that has never marked, or an upgraded one), or a cursor recorded under
  other resolver or evidence versions (`meta.execution_dependents_versions`,
  for example after `execution-v5`), every derivative is compared with the
  catalog-wide digests;
- otherwise the source changes past the cursor are walked: their components,
  the components that shared a changed conversation's stored digest, and the
  components the changed conversations were recorded members of. That covers
  new claim endpoints, late originals, reverse links, aliases, corrections,
  splits and deletions.

A ledger without the digest column (every released catalog, until the
labels or events run migrates it) is pending in full. The migration marks
`''` rows dirty and records the members of components whose digests still
hold.

**Write-time propagation.** `markExecutionDependents` runs in the writing
transaction of ingest, every backfill batch and recovery, and at the start
of each labels or agent events run. It marks dirty what the freshness result
names, then records the cursor and versions. The label facts follow through
their dirty triggers; the events marks also go to
`agent_events_execution_stale`. Reads never write.

**Unmarked changes.** A writer that does not mark leaves its changes past
the cursor until the next mark: library transfer, copies, repairs, or
another process's older executable. Readers compute what that mark would
mark (`executionUnmarked`, the same function, read only) and show it as
pending, so they need no writer to cooperate. SQL built without the catalog
uses `executionUnmarkedFallbackSQL`: every built conversation while anything
is unmarked. It is broader, never narrower.

**Caches.** The inverse index and catalog-wide digests are cached per catalog
file and source generation. The unmarked set is cached per catalog file,
ledger, propagation state (generation, cursor, versions) and ledger stamp:
triggers count every write to a ledger's rows by any executable
(`execution_dependents_stamp`). Entries are admitted only from a committed
read-only snapshot. A write transaction may look entries up but never stores
one: its rows may roll back, and the next commit can reach the same
generation with other rows (`TestDependencyCachesIgnoreRolledBackWrites`).

The inverse index is patched rather than reread. A reader at a later
generation than the cached entry (a write transaction included) copies it
and rereads only the conversations changed since it, as
`execution_source_changes` records them. Every inverse lookup belongs to
one owning conversation, so this equals a full read
(`TestDependencyInversePatchMatchesRows`). Past 5,000 changed conversations
it rereads in full.

The catalog-wide digests are patched the same way
(`patchDependencyDigests`). A digest can change only in a component that
holds a changed conversation, now or at the cached generation; this is the
argument marking rests on. So:
- those components (the changed conversations, and every member of a
  changed conversation's former component) are walked again;
- every other digest is kept;
- the same test compares the patch with a full read at each step, inside
  the write and committed;
- past 5,000 changed conversations, or a walk over the dependency limit, it
  reads in full.

Two further lookups use the cache:
- A conversation's freshness (`storedExecutionCurrent`, the conversation
  reader's summaries) uses the cached digests when they are at its
  generation, and walks its component otherwise.
- Before marking a catalog with no cursor, propagation reads the digests
  from a committed snapshot, so the writer patches them rather than reading
  the whole catalog.

The legacy launch-result inputs read only the results of each launch call,
through the call index (`messages_call_idx`), never a launcher's other tool
results. Claude agent keys are looked up once per root, not once per key.

**Cached views.** Every change to the unmarked set moves
`query_cache_generation`: source writes through its triggers, ledger
publication likewise, and each mark advances it as well. A mark never
advances `execution_source_generation`, so the resolver caches are
unaffected. `TestCursorOnlyMarkRefreshesCachedViews` warms a cached view and
covers two cases: an edit that moves no digest stays current, and an unmarked
claim-only link turns the warmed view pending.

Reads that serve these derivatives, and their freshness before any rerun.
`TestDerivativesExposeEvidenceChangedElsewhere` covers the following cases:
- an ingest;
- a writer that does not mark;
- a late original named only by a copy claim;
- a claim-only parent (`parent_id` NULL);
- a deleted claim carrier with no derivative of its own, after a restart;
- no cursor;
- changed versions;
- `''` digests, with and without a later change;
- an older executable publishing after a warm read;
- released ledgers without the column;
- a parent's original arriving for a fork's copy (the copy moves from
  original to inherited), and that original deleted again;
- an original's session membership changing;
- a typed copy declaration whose named source is present, when only the
  source's original of the copied event arrives;
- an unrelated append to the parent, which leaves the fork's digest equal
  and the fork current.

Each case checks every built conversation against the transcript (the table,
status and event withholding), then the ordinary runs.

| Reader | What it reports |
| --- | --- |
| Label summaries (conversation reader) | `pending: true` when dirty or the stored digest is not current (`''` and missing columns are not current) |
| Agent events in the conversation reader (`agentEventsAttacher` summary) | `pending: true` when dirty or the stored digest is not current |
| Label table (`message_labels` Query Table) and the label facts that label queries and dashboards read | labels of a dirty or unmarked conversation read as `unlabeled` |
| Labels status (`LabelsStatus`, `pharos labels status`) | `dirty_conversations` includes unmarked conversations |
| Ask label coverage | counts only conversations that are neither dirty nor unmarked |
| Agent events status (`agentEventsStatus`) | `conversations_changed` (dirty or unmarked), `conversations_stale` |
| Fixes (`FixesOverview`), Safety (`SafetyOverview`), Ask agent-event signals | `events_freshness` / the lead: `pending` with counts while anything is dirty, unmarked or withheld, else `current` |
| Agent Experience datasets (`agent_events`, `agent_event_messages` Query Tables), the explorer, ribbon and item lookup | rows of conversations in `agentEventsWithheld` are left out: marked stale (cleared at publication) or unmarked. Each query-v3 response carries `coverage` (`agentEventsCoverage`: `state`, `pending_conversations`, `withheld_conversations`), so missing rows read as pending, not as no events. Stored rows and inference are kept. |

Both ledgers rebuild once after upgrading, since their structures include
the digest. The occurrence version is part of the recorded versions
(`executionDependentsVersions`), so a catalog marked before occurrence facts
were in the digest compares every derivative on its next mark and reads
pending until then.

`TestUnmarkedSetFollowsEventLedgerWrites` checks the ledger stamp on its own.
An events-only publication by another executable, with no labels write and
an unchanged source generation, turns a warmed unmarked set pending and
moves `query_cache_generation`. The next run clears it.

## Measurements

**Measured implementation baseline, occurrence facts included**, 2026-11-07.
These timings precede the review corrections described above; no new
library-scale runtime benchmark was run for that final source. The final
legacy acknowledgement query was checked with library statistics and still
uses the call index, without scanning unrelated result bodies.
`TestExecutionEvidenceProbe` (build tag `probe`) with
`PHAROS_EXECUTION_PROBE_FAMILIES=400`:
- the library: 5,001 conversations, 14,601 messages and 5,201 sessions in
  401 components; the largest family has 1,801 conversations;
- the conditions: one run on the development Mac (M3 Ultra, 28 cores),
  under unrelated load (load average 18–24), in a temporary catalog.

| Step | Legacy rows only | Typed evidence current |
| --- | --- | --- |
| Catalog-wide digests, cold (admitted from a read-only snapshot) / warm | — | 120 ms / 0 |
| Catalog-wide digests in a write transaction after it writes (computed, not stored) | — | 115 ms |
| Inverse index in a write transaction after it writes (patched) | — | 2 ms |
| Component walk: typical / largest family | — | 5 / 79 ms |
| Publication guard + members: 200 conversations / largest family | — | 14 / 88 ms |
| Read freshness, one conversation | 8 ms | 5 ms |
| Retained-row backfill, all 5,001 conversations (adaptive batches) | 2.70 s | — |
| Backfill again (nothing pending) | 2 ms | — |
| Ingest a late transcript, then its family's walk | — | 18 ms |

The legacy-only rows above the backfill were not kept from this run's
output; the previous run (before occurrence facts) took 73 ms cold for the
catalog-wide digests, 45 / 120 ms for the walks and 7 / 65 ms for the guard.

Write-time marking (events published for every conversation: 51 ms) and the
read side:

| Step | Time | Result |
| --- | --- | --- |
| Ingest an unrelated transcript | 12 ms | marks only itself |
| Ingest a child of a typical family | 20 ms | marks the family (9) |
| Ingest a child of the largest family | 151 ms | marks the family (1,810) |
| Unmarked set after a non-marking change, typical family: cold / warm | 6 ms / 4 ms | 8 |
| Unmarked set after a non-marking change, largest family, cold | 89 ms | 1,809 |
| Unmarked set with no cursor (whole catalog compared), cold | 124 ms | 1,817 |
| First mark with no cursor, in a write transaction | 14 ms | marks all 1,817 |
| Occurrence claims, a copy claim per child: catalog-wide / largest family | 48 / 19 ms | the synthetic copies match no messages, so this times reading and indexing the claims |

Occurrence facts added about 40–65 ms to the catalog-wide and largest-family
reads at this size. The synthetic library has no matching copies, so this is
the cost of reading claims and ancestry rather than of classified copies.

### Library-scale resolution (legacy path, 47k backup clone)

Core ran this before the review corrections on a disposable APFS clone of a
110 GB library backup, never the live catalog:
- 47,056 conversations, which resolve to 47,755 executions;
- 8 identity conflicts covering 16 executions;
- 2,637 missing parents and 984 proven copies.

The clone's typed evidence was from v2, so every conversation read as not
current at v3. **This measures the legacy path.** The first run was on a
fresh clone; the second reused it warm.

| Step | Fresh clone | Warm rerun |
| --- | --- | --- |
| Open + Initialize upgrade (one-time: triggers, authority column, ledgers) | 16.1 s | already upgraded |
| Full resolution at process start | 8.9 s | 2.0 s |
| Catalog-wide occurrences at process start | 16.8 s | 4.6 s |
| Cached resolution / cached roots + occurrences | 0 / 0.58 s | 0 / 0.70 s |
| Derived write + resolution | 10 ms | 3 ms |
| Commit to the busiest transcript + resolution + occurrences | 0.82–0.85 s | 0.91–1.48 s |
| Ingestion (40 commits of 200 messages, 3 global readers): commit p50 / p95 / max | 239 / 653 ms / 25.8 s | 280 / 530 / 596 ms |
| Same, reader p50 / p95 / max | 1.42 s / 27.2 s / 27.3 s | 2.03 / 2.30 / 2.30 s |

Things these runs do not establish:
- **The fresh-clone stall is unexplained.** The writer and two readers
  stalled once, for 25–27 s, on the first writes to the fresh clone. It did
  not recur warm. First-write I/O on the clone is a guess; it was not
  instrumented.
- **Execution trigger overhead is unresolved.** On a 2,000-message commit,
  the single samples disagree (+5% vs +39%).
- **The typed v3 path is unmeasured on the full library.** That needs the
  one-time backfill, which an earlier backfill of the same library took
  10m13s.
- **Memory scope differs between probes.** Before the dependency optimization,
  the combined resolver, occurrence and dependency probe peaked at 2.96–3.06 GB.
  The final dependency-only probe below peaked at 0.25 GB. The combined process
  peak was not remeasured after that optimization.

These are single runs on one machine. The probe logs are not kept in the
repository.

### Dependency digests on the same clone (legacy path)

Measured 2026-11-07 on the same clone, opened read-only (`immutable=1`), in
its own probe process. The page cache was warm, and earlier runs had read
the clone. The library has 47,056 conversations in 30,881 components; the
largest components have 3,088, 2,091 and 1,067 conversations.

| Step | Before dependency optimization | After dependency optimization (before review corrections) |
| --- | --- | --- |
| Catalog-wide digests | 11.1 s; 2.25 GB allocated | 3.25–3.43 s; 653 MB allocated |
| of which legacy launch results | 8.27 s (458,753 tool results, 4.5 GB of text) | 0.54 s (the 8,161 results of launch calls, 66 MB) |
| of which occurrence facts (core) | 2.01 s | 2.06 s |
| Patched catalog-wide digests, one changed conversation in a 1,067 / 3,088 family | — | 0.17 / 0.23 s |
| Patched catalog-wide digests, the 10 / 100 / 1,000 most recently changed conversations | — | 0.46 / 0.60 / 2.24 s |
| Inverse index, first read | 7.9–9.0 s | 0.61 s |
| Component walk (publication guard, write-time mark), 1,067-member family | 1.04 s; 935 MB allocated | 0.17 s; 23 MB allocated |
| Component walk, 3,088-member component | — | 0.24 s |
| Process maximum RSS for the probe | 2.50 GB | 0.25 GB |

The fixes behind the before/after columns:
- **Launch results:** each launcher's tool results were read whole and
  filtered afterwards, instead of reading only the launch calls' results.
- **Claude agent keys:** each key read every subagent of its root, so cost
  grew quadratically with a family's size.
- **Catalog-wide digests:** they were recomputed in full each generation;
  they are now patched.

Core's validation probe ran the old code on a cold first read in a process
that had already resolved the catalog. It took 55.1 s for the catalog-wide
digests and 9.3 s for the busiest family's walk (which included the first
inverse index read), with a 2.96 GB peak RSS for both probes together.

**Not measured:**
- **A cold page cache with the final code.** The text read is down from
  4.5 GB to 66 MB, but the time was not measured.
- **The typed v3 path.** It drops the legacy launch-result reading
  altogether; it was not measured on this library.
- **The adaptive backfill hold on a library copy.**
  `TestExecutionEvidenceBackfillBudget` bounds it near one second. Fixed
  500-conversation batches held the writer 4–17 s on a backup copy.

## Limitations

- The dependency digest is conservative: any evidence change in a component
  rebuilds every member's items (not their inference). A component larger than
  `executionDependencyLimit` (20,000 conversations) uses the catalog-wide
  digest, which is exact but reads the whole catalog. Inside a write
  transaction that read is uncached.
- Pending is per component: an unmarked change holds back its whole
  component (and its former one) until the next run, as marking does.
- Derivatives published by an executable from before members were recorded
  have no member rows. The migration records members for those whose digests
  still hold and marks the rest, so this matters only for a still-older
  executable publishing again later. Its rows have `''` digests, which read
  pending anyway.
- Message text edits in an ancestor are not part of the digest. Its occurrence
  facts are (event IDs, memberships, originals). Forks follow their source's
  text through the ledgers' existing follow rules.
- A full catalog-wide digest read costs about 3.3 s on the 47k library, page
  cache warm, and core's occurrence facts are about 2 s of it. A process
  pays it once:
  - for its first catalog-wide read;
  - after more than 5,000 changes;
  - when a write-time mark in a catalog with no cursor finds nothing cached
    (an ingest's own mark: propagation reads ahead of the writer).
  Later generations patch it.
- **Agent events of a pending conversation are withheld, by policy.** A
  conversation is pending when it is dirty, marked stale or unmarked.
  - Its stored event rows and inferences are kept, and become visible again
    once a run republishes them against current evidence.
  - Pending is reported per response, not per row. Query-v3 responses for
    `agent_events` and `agent_event_messages` carry `coverage`
    (`agentEventsCoverage`: `state`, `pending_conversations`,
    `withheld_conversations`), and the summaries carry `events_freshness`.
  - Callers show it: Agent Experience dataset notes, the explorer table and
    sidebar, the ribbon's "Events pending" badge, the item detail panel, and
    the Fixes and Risky actions notes.
  - Labels of a pending conversation read as `unlabeled`, and label
    summaries carry `pending: true`.
  - Timeline lanes show their pending state. Its summary uses
    `events_pending_sessions` from the full snapshot, so collapsed, paged or
    unloaded sessions still contribute to the notice in the Timeline and
    compact reader preview.

- No adapter emits an explicit "no copied history" declaration
  (`historyDeclaresNoCopies`: `mapping: "none"`). Claude sidechains and Codex
  `thread_spawn` children without `forked_from_id` are counted as recorded when
  read from the provider. That does not establish whether history was copied.
  Evidence derived only from retained rows still reports
  `retained-history-undeclared/v1` until the provider is read again.
- A Codex ordinal names rollout items, which stored messages follow only
  approximately, so a copy whose original is missing stays unresolved rather
  than inherited.
- Cursor evidence for composers Cursor has since deleted is unrecoverable.
- Providers other than Claude, Codex, TL1 and Cursor have `unclassified/v1`
  identifier policies; their copies match on observation only.
