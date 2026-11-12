# Shared execution resolver — design for review

Status: implemented and independently reviewed, 2026-11-07. The original feature passed Go fast/full and targeted race checks after the acknowledged-launch cycle correction and resolver-coupled cache upgrades. Those results precede the subsequent master integration; validation below distinguishes the two source states.
Implementation and historical coverage: [evidence/backfill manifest](execution-evidence-backfill.md).
Companion: [execution identity audit](execution-identity-audit.md).


## Implementation outcome

The shared resolver now supplies execution bindings, parentage, labels and
ordering to the explorer, Timeline, usage lineage, quick statistics, activity,
parallelism and labeling. Copied occurrences, launch attempts and continuation
segments have separate representations. Labels and events record execution and
occurrence dependencies, reject stale publication, and expose pending coverage
while attribution is rebuilt. Timeline ancestry and URL restoration use stable
identities rather than numeric outlines.

Three delegated owners implemented the core, evidence and consumer changes and
reviewed one another's work. The final review corrections cover:

- **Launch identity:** compare authority across every target of a launch,
  including an accepted handle whose transcript is not retained yet. Weaker
  contradictory links cannot merge separate executions. Available
  acknowledgements are read consistently before and after evidence backfill.
  Resolver `execution-v6` and evidence `execution-evidence-v4` also recognize
  the validated harness usage footer of a completed `Agent`/`Task` call, keeping
  child identity and reported usage consistent without inflating aggregate totals.
- **Activity bounds:** compute spans from owned observations. Raw session
  envelopes are only a reported fallback for streams without timed observations;
  copy-only history cannot establish a span or historical active-run membership.
- **Requests:** connect request observations to their first and last usage
  messages. Proven copies are excluded from own request counts and activity;
  reconciled tokens, cost and request-row accounting remain separate.
- **Persisted attribution:** invalidate parallelism and origins when execution
  evidence changes, including native event-ID/locator corrections with unchanged
  parent spans. All-withheld event datasets show pending coverage. Retained
  evidence corrections, partial backfill groups and materialized Claude links
  cannot leave unsupported derived parentage current.
- **Complete occurrence consumers:** parallelism timing and fingerprints use
  the same snapshot occurrence index as the other consumers, including
  resolver-derived copied launches and the requests carried by those messages.
  Ancestry cycles retain unique observations and qualify ambiguous shared
  events rather than treating an observation as its own copy. The fallback
  launch matcher requires proven one-way ancestry, including a completed reverse
  search. Launches on a cycle that acknowledge the same child retain their
  execution bindings and issuing evidence; the child's parent stays ambiguous.
  A search stopped by its bound is never evidence that reverse ancestry is absent.
- **Transfer references:** request usage links and skill evidence/body links
  retain their native message targets when catalogs use different local IDs.
  Missing links remain missing; transfer does not rebuild accounting rows.

Validation on the original feature before master integration:

| Check | Result |
| --- | --- |
| Go fast and full suites (full with `-count=1`, including vet) | Passed after the final acknowledged-launch and cache-upgrade corrections |
| Earlier four unchanged user-provided overlay reproducers | 4/4 passed before the acknowledged-launch follow-up |
| Earlier follow-up reproducers: launch precedence, origins, copied launches, cycles and parent corrections | All 7 passed before the acknowledged-launch follow-up; private overlays adapted helper signatures only, preserving every fixture and assertion |
| Acknowledged same-child launch cycle reproducer | Failed before the fix and passed unchanged after; independent review verified nonempty child bindings and both candidate issuers |
| Final focused races: identity, ordering, cyclic ancestry, concurrent caches and activity/origin upgrades | All 6 root cases and their 24 subtests passed, including all 12 launch-cycle/control/bound cases |
| Earlier probe-tagged Go vet | Passed before the acknowledged-launch follow-up |
| Web check: types, generated contracts, build, bundle and unit tests | 1,421 passed; 3 environment-dependent `age` skips; web sources and bundles unchanged by the final Go follow-up |
| Targeted Chrome / WebKit pending-event routes | 6/6 each; Chrome's 16 shared-fixture pending routes also passed |

The subsequent integration with master at `521b5724` passed the web check and
full card/cover matrix (1,567 tests each, with three environment-dependent
`age` skips), 26 focused browser structure cases and eight restoration cases.
After integrating master's scoped token evidence, 337 focused Go cases passed,
including all existing footer acceptance/rejection cases and a retained-row
compatibility check. A private mutation reproduced the ordinary Codex tool-result
misclassification; the final typed producer restricts footer recognition to
`delegation_result`, matching token attribution and the retained-row reader.
These integration checks are separate from the original full Go and race
results above.

The final parallelism sweep compares affected stored fingerprints through the
shared source journal and occurrence scope, including changes to copied launches.
It conservatively compares all rows when the earlier snapshot is unavailable,
and builds no occurrence index when there is no stored activity to compare.
Parallelism and origins record their own derivation version together with
`executionResolverVersion`. A resolver upgrade therefore supersedes stored
projections once even with unchanged source data; subsequent opens stay idle.
Scope discovery still walks the compact in-memory graph; these follow-ups have
no new library-scale benchmark. Request accounting and raw source records remain
unchanged. Transfer tests cover both request usage links and skill message links
when destination catalogs use different local IDs.

Earlier focused races also passed for retained evidence, origins and the
selected derivatives request-link publication case. The earlier full card/cover
unit matrix passed; it was not repeated for the review corrections because those paths did not change. The full browser suite
was not rerun. Broader earlier runs retain two failures also reproduced on
unchanged `HEAD`: the classic event legend overflows at 320 px in both engines,
and WebKit's thinking-card hover can target an event mark instead. Neither was
hidden or treated as passing.

The original reported family is covered by captured evidence and permanent
production-API fixtures. Its exact IDs are absent from the available older
backup, so this work does not claim a fresh retest against the live library.
The live catalog and provider stores were not read or migrated during
implementation. Historical evidence limits, maintenance commands and measured
cold/warm/update costs are in the [evidence/backfill manifest](execution-evidence-backfill.md).

Review incorporated: evidence provenance/backfill, ongoing derivative
invalidation, occurrence matching without native IDs, and removal of behavioral
dependencies on outlines. These are required migration work, not follow-ups.

## Decision requested

Adopt one resolver for execution identity, ownership, and occurrence provenance.
Hierarchy, Timeline, analytics, quick statistics, concurrency, and labeling
would consume projections of that result. Provider readers would retain and
normalize evidence; presentation and analytics code would stop independently
deciding what constitutes an execution.

The user has decided to remove `1.x` numbering for sub-sessions. Stable numeric
outlines are therefore **not a requirement**. Legitimate sub-sessions must
remain visible, keep the same parent, and appear in a consistent order when
switching the reader or moving between Hierarchy and Timeline. Removing
numbering does not remove those requirements.

This proposal preserves archived source records and existing accounting facts.
It proposes a shared interpretation, not a destructive rewrite of transcripts.

## 1. Product contract

For a fixed evidence generation:

1. A launch handle, its child transcript, and copies of that launch can refer
   to one execution; their presence must not multiply the agent count.
2. A rejected launch is an attempt, not an execution. An acknowledged agent
   that subsequently fails its task is still an execution.
3. A continuation adds a transcript segment to an execution, not a child agent.
4. Every execution has one resolved parent, is a known root, or has explicit
   unresolved ancestry. Missing evidence is not an observed depth of zero.
5. Navigation, filters, pagination, and workspace boundaries do not change
   identity, parentage, label selection, or relative sibling order.
6. Copied history can remain inspectable as context, but does not become the
   inheriting agent's own messages, tool calls, requests, or activity span.
7. Tokenless agents count when there is execution evidence. Tokens are not
   the test for existence or identity.
8. Different scopes can have different totals. Overlapping scopes must agree
   about the executions and occurrences they share.

Unknown attempts, unmatched transcripts, conflicting claims, and coverage gaps
remain inspectable. Excluding something from a confirmed execution count does
not authorize hiding its source record.

## 2. Objects and boundaries

| Object | Meaning | Example |
| --- | --- | --- |
| Source observation | A preserved record in a transcript, session table, or ledger | The copied spawn message in Peirce's transcript |
| Launch attempt | An originating request to create an agent, with an outcome or unknown outcome | `spawn_agent` rejected by a thread limit |
| Execution | One agent run, possibly represented by multiple source streams | The agent assigned `explore`, nicknamed Peirce |
| Transcript segment | A navigable source transcript belonging to an execution | A resumed or continued Codex conversation |
| Occurrence | One physical message, tool invocation, or model request, possibly observed more than once | The parent's spawn call copied into child history |
| Accounting fact | Reported or reconciled usage and its accounting scope | An hourly usage row or cumulative checkpoint |

An attempt is not automatically an execution. An execution can have several
transcript segments and accounting streams. A single tool invocation can have
several observations. These distinctions must be represented directly rather
than inferred differently by each consumer.

Provider-specific parsing is still necessary. Providers may disagree about
what their fields mean; Pharos must have one agreed interpretation of the
normalized evidence.

## 3. Architecture

```text
Preserved transcripts, session records, identity links, usage ledgers
                              |
                 Provider evidence extractors
            (typed claims, locators, namespace, provenance)
                              |
                 Shared execution resolver
       identity + attempts + parents + occurrence ownership
                    + presentation metadata
                              |
                 Immutable, versioned result
                  /           |           \
        Family navigator   Activity     Analytics / labeling
        + reader links     + Timeline   + accounting projections
```

Keep the implementation in the existing `internal/archive` package. Suggested
boundaries are `execution_evidence.go`, `execution_resolver.go`,
`execution_occurrences.go`, and `execution_projection.go`; filenames are
provisional. No Query Table dependency change is required for this design.

The resolver takes normalized evidence as input. It must not depend on React,
the selected workspace, a usage query's retained rows, or current UI settings.
One pure resolution core should work for both live catalogs and offline
labeling snapshots.

## 4. Proposed result model

The following is an interface sketch, not a prescribed database schema:

```go
type ExecutionResolution struct {
    Revision     ResolutionRevision // resolver version + evidence generation
    Executions   map[ExecutionID]Execution
    Attempts     map[AttemptID]LaunchAttempt
    Bindings     map[SourceRef]ExecutionBinding
    Occurrences  OccurrenceIndex
    Conflicts    []IdentityConflict // incompatible identity/creation claims
    Coverage     ResolutionCoverage
}

type Execution struct {
    ID           ExecutionID
    Presence     PresenceEvidence   // observed activity or verified creation
    Parent       ParentResolution
    Segments     []TranscriptRef
    Label        ResolvedLabel
    OrderKey     ExecutionOrderKey
}

type ParentResolution struct {
    State        ParentState        // root, resolved, ambiguous, missing, cycle
    ParentID     *ExecutionID       // set only for a resolved edge
    Certainty    EvidenceStrength   // explicit or inferred; separate from state
    Evidence     []EvidenceRef
    Candidates   []ParentClaim      // conflicting/rejected claims stay available
}

type ExecutionBinding struct {
    ExecutionID  *ExecutionID
    State        BindingState       // resolved, attempt_only, inherited, unresolved
    Evidence     []EvidenceRef
}
```

`SourceRef` must distinguish conversation, agent-session, launch-message,
tool-call, and request locators. `OccurrenceIndex` can be a lazy lookup service;
the entire message corpus does not belong in each cached graph.

Keep existence, parent resolution, and data coverage as separate dimensions.
An observed agent with a missing parent is real and countable, but its root
cohort and execution depth are unknown. A verified launch can establish a real
agent even when no child transcript or usage survived.

An occurrence lookup returns its canonical occurrence ID, owning execution,
observation role (`original`, `inherited_copy`, `duplicate_observation`, or
`unresolved`), supporting evidence, and original source navigation target.
Copy timestamps never replace the original event's time or extend the child's
self-activity bounds.

Canonical occurrence identity, ownership, original time and original navigation
target can each be unavailable; an observation locator alone does not resolve
them. Return the surviving observation targets as well.

### Identity and compatibility

Use provider/account/native execution identity when available. Embedded
executions without a native thread identity use a stable originating launch or
stream anchor scoped to their source execution. Never use labels, numbering,
workspace order, or token totals as identity keys.

All source anchors remain lookup aliases. If an acknowledged handle later gains
a native transcript, resolution can choose that stronger canonical anchor while
old links continue through the binding map. Resolver versions are not part of
execution IDs. This guarantees stable lookup for retained source locators;
it does not pretend that contradictory new evidence can never correct or split
an identity decision. A split must return an explicit ambiguous result where
an old locator no longer identifies one execution.

Keep existing conversation/message URLs. Legacy `delegation_session` values
resolve through source bindings, including the current session IDs and
synthetic ancestor/external references. Do not require users to rewrite links
or discard saved disclosure state as a condition of migration.

Apply the same alias restoration to `delegation_collapsed`,
`delegation_expanded`, and session-derived disclosure keys such as
`delegation-labels:<session-id>:session`, through the shared view-state bindings.
Distinct old IDs that resolve to one execution normalize to one state target;
explicit canonical-key values take precedence over aliased values. If conflicting
aliases have no canonical value, collapse wins for expansion state. A split or
unresolved alias stays available as unresolved state; never apply it to an
arbitrarily chosen execution. Migration must preserve unrelated disclosure keys.

## 5. Resolution rules

### Evidence extraction and discovery

Extract compact typed claims from provider metadata, originating launch calls
and results, recorded session parents, explicit child links, mirror identities,
continuation metadata, and inferred command launches. Each claim retains its
source locator, parser version, identifier namespace, and evidence strength.

Claims also distinguish native assertions, parser-derived inferences, legacy
materializations, and synthesized fallbacks, with the derivation rule/version
and supporting observations. A stored relationship is not automatically a
native assertion. Preserve identifier kind separately: a genuine provider event
ID, a provider call ID with its documented scope, and a synthesized `line-N`
locator have different matching semantics. Record copy boundaries and source
ordinal mappings when providers supply them.

Recognizing a result envelope is a provider parser's job. Report prose that
mentions `agentId:` is not a launch acknowledgement. Suffix matching must stay
inside the provider/account and identifier format that defines that suffix.
Mixed-provider command launches are a different typed relationship, not an
exception to namespace validation.

Discovery must follow every supported ownership edge, including explicit child
links, and query native execution IDs directly even when transcript parents
are absent. Incoming lookup must not require the child to already have the
parent hint that resolution is meant to recover.

Provide a compact evidence index for forward and inverse lookups. The initial
implementation can build and cache that index once per evidence generation;
it must not scan all transcript bodies on each workspace request. If cold-build
cost requires persisted indexes, those are versioned, rebuildable derivatives,
not another authority for ownership. Labeling snapshots must carry equivalent
evidence or declare the missing capability.

### Extraction and historical backfill gate

Current ingestion has already interpreted or removed some evidence. In
[Claude lineage reconciliation](../internal/archive/claude_lineage.go), a child
without an accepted launch edge receives the root as its parent. In the
[Cursor adapter](../internal/archive/adapter_cursor.go), copied messages and
echoed subagent records can be suppressed before catalog insertion. Existing
normalized rows alone cannot recover those distinctions.

Before resolver rollout, inventory each provider's evidence retention and
produce a versioned migration manifest:

| Historical condition | Required treatment |
| --- | --- |
| Retained native evidence and recorded derivation provenance are sufficient | Extract/backfill typed claims from retained records; no provider reread required |
| Legacy Claude parent/depth lacks provenance separating accepted edges from root fallback | Re-extract lineage from retained provider input; if unavailable, mark the legacy edge unverified rather than treating it as native proof |
| Cursor copy boundaries, ordinal mappings or suppressed records are absent from retained catalog evidence | Re-read affected provider groups with the revised extractor/index version; retain evidence before filtering |
| Codex `NativeID` may contain a generated `line-N` locator without an explicit identifier kind | Recover identifier kind from retained source and extractor version; if it cannot be established, use observation-only matching semantics |
| Original evidence is unavailable | Preserve existing records and expose the provenance/coverage gap; do not reconstruct certainty from the old graph |

The inventory determines affected records and reindex scope, including source
groups whose old normalization omitted entire observations. Merely bumping the
resolver version cannot restore that evidence. Future ingestion must retain
typed provenance before legacy filtering/reconciliation; raw source and usage
preservation remain required. Backfill must not rewrite transcripts to fit the
new graph. Provider-input fixtures must traverse extraction, ingestion,
resolution and consumer APIs, alongside the smaller resolver-only fixtures.

### Identity and inherited history

Resolve strong mirror/continuation relationships and history-origin markers
before promoting launch candidates to executions. Identify inherited launch
occurrences through their origin across the ancestry chain, including when an
intermediate transcript was compacted. Use namespaced native event IDs and
retained history relationships; equal text or equal call ID in unrelated
conversations is insufficient.

Identity reconciliation must also respect evidence of distinct executions. A
verified creation edge from A to B constrains A and B to remain distinct; a
weaker continuation/mirror claim must not union them into one self-delegating
execution. Likewise, an identity claim must not erase a verified delegation
elsewhere in the component. Reject a weaker incompatible claim with its reason.
If equally authoritative claims disagree, return an explicit identity conflict
containing the source bindings and incompatible claims, and mark execution
cardinality and affected ancestry unresolved. Keep all sources navigable; do
not report either partition as a confirmed execution count or let a consumer
choose one. Reconciliation must examine these constraints before committing
aliases, rather than handling the resulting self-edge as an ordinary cycle.

### Occurrence matching without native event IDs

The [Codex adapter](../internal/archive/adapters.go) can put `line-N` in
`NativeID` when a source event has no ID. It locates an observation within one
source revision; equal line numbers across transcripts prove no equivalence,
and copied events can move to different lines.

Use a genuine event identity only within the provider's declared namespace.
Otherwise require a retained copy/history boundary and unambiguous source
ordinal mapping, or an equivalently explicit provider relationship, to resolve
copies. Ordered content matching can corroborate that mapping, but repeated
identical content, text hashes, timestamps or synthetic locators alone cannot
prove one physical occurrence. Compaction markers establish only the coverage
and mappings they actually retain; they do not authorize fuzzy deduplication.

When those signals are insufficient, preserve separate observations with
unresolved equivalence/ownership. Affected canonical event counts and activity
ownership report partial coverage rather than confidently including or
deduplicating the candidates. Transcript inspection still exposes every
retained observation, and accounting follows its separate preservation rules.

Where copy provenance is known but only the copy survives, navigate to that
surviving observation and identify it as inherited evidence. Known original
ownership can remain attached to an unavailable source anchor; unknown
ownership remains unknown. If copy provenance is itself uncertain, retain the
unresolved role. Do not promote a surviving copy into the inheriting execution's
self activity or substitute its copy time for the original time.

### Launch attempts

Classify each originating attempt:

| Evidence | Resolution |
| --- | --- |
| Explicit creation rejection and no independent execution evidence | Failed attempt; no execution |
| Verified returned handle, including a later task failure | Execution exists; transcript/usage can be unavailable |
| Independent observed stream activity | Execution exists even without tokens or a retained acknowledgement |
| Launch call only, outcome absent | Unresolved attempt; visible, excluded from confirmed execution count |
| Copied launch occurrence | Binds to its original attempt; creates no new agent |

When newly discovered acknowledgements expose more transcripts or history
origins, use a bounded worklist over evidence IDs and recompute affected
decisions. The result must depend on the complete admitted evidence, not the
order in which rows happened to load. Do not make irreversible alias unions
while streaming partially examined claims.

### Parentage and certainty

Proposed precedence for compatible claims:

1. Validated originating launch ownership tied to a specific execution,
   including a supported explicit child link or verified acknowledgement.
2. Recorded session-parent relationship.
3. Provider-typed native delegation-parent relationship.
4. Inferred command launch.

This ordering applies after provenance validation. A synthesized root fallback
or unverified legacy materialization does not qualify as a recorded native
session-parent assertion; it remains a discovery hint until corroborated.

Equally authoritative claims naming different canonical parents produce
`ambiguous`; later row order does not break the tie. Preserve weaker conflicting
claims as diagnostics. Validate legacy persisted child links through the same
namespace/evidence rules rather than assuming every old link is infallible.

A continuation is an identity/segment relationship and adds no delegation
depth. Provider metadata should distinguish it from delegation. Existing
Codex depth-zero parent conventions can be supported as an explicitly named
compatibility rule; generic `parent_id` alone must not acquire a universal
meaning across providers.

Detect cycles after identity reconciliation. Mark the affected ownership
component unresolved; never cut an arbitrary edge and call the result a known
root. Descendants can retain a known direct parent while their family root
and absolute depth remain unknown. Keep semantic depth optional and separate
from UI indentation, which can still render an unresolved component.

## 6. Visibility, labels, and order without numbering

The navigator projects a resolved family. Choosing another reader within that
family preserves the legitimate execution rows, parent relationships, and
relative sibling order. Sibling metadata can be loaded without fetching sibling
transcripts or accounting. Selection must not implicitly switch from a family
scope to a selected-workspace-only definition of the tree.

An explicit scope/filter can omit rows. Pagination and disclosure can defer
them. Such operations must be visible in the scope/coverage contract, preserve
the order of common rows, and not make omitted agents cease to exist in the
underlying graph. When a family exceeds a limit, paginate its compact metadata
and report incomplete coverage rather than quietly rebuilding a smaller tree.

Continuation segments remain independently navigable, in segment order, under
their execution. They are not counted or nested as additional child agents.
Failed and unresolved attempts remain accessible as attempt/source evidence;
they do not occupy falsely confirmed agent rows.

The resolver chooses presentation metadata once:

- **Label:** preserve supported source names and overrides in their conversation
  scope; otherwise use the first classified human-written words, then a recorded
  launch/request assignment, readable nickname or explicitly scoped task context.
  A missing retained request is stated explicitly; native IDs remain metadata,
  never primary labels. [Naming and backfill](conversation-naming.md) describe
  the compact cache and source freshness rules used by resolver `execution-v8`.
  Nicknames remain available as metadata. The same agent is not renamed merely
  because its transcript is now selected.
- **Order:** return a deterministic total sibling key from originating launch
  evidence, with the execution's own first activity as fallback. Exclude copy
  timestamps and query-local metrics. A concrete initial comparison is
  `(time_missing, launch_or_first_activity_time, origin_stream_key,
  position_missing, source_position, stable_execution_anchor)`.
- **Independence from projection:** derive that key before filtering and use
  it in Hierarchy, Timeline, child pickers and continuation navigation.
  Missing times use the stable anchor fallback in every view.

Ordering is stable for unchanged evidence, not frozen against genuine evidence
corrections. A new originating timestamp or corrected parent may change the
next generation's order; a reader selection, copied timestamp, or scope-local
activity calculation may not.

There is no new outline field or requirement to preserve `1.x` labels. Existing
outline-based audit cases become visibility, parentage and relative-order
cases; numeric-label assertions should be retired with the numbering UI.

Removing outline values requires a behavioral migration. Today
[Timeline lane filtering](../web/src/delegation.tsx) uses outline prefixes to
retain ancestors of active descendants, and
[branch rendering](../web/src/timeline-label-model.ts) falls back to reconstructing
parents from outlines. Replace both with resolved parent IDs and an explicit
ancestor closure, loaded from the same resolution revision before filtering or
pagination. Keep an inactive parent visible when a descendant is active.
Distinguish omitted known ancestors from unresolved ancestry and carry metadata
or coverage for them; never infer either from a string prefix. UI indentation
and branch rails are layout projections, not semantic depth. Remove outline
fallbacks before retiring the outline payload, coordinating with the numbering
removal work without preserving numeric labels.

## 7. Accounting and scope

Separate execution identity from accounting overlap. One execution can have
several independently reported usage streams. Resolving a handle and transcript
to one execution does not authorize deleting a positive-token stream, summing
overlapping checkpoints, or manufacturing zero usage for an unqueried child.

Proposed migration policy:

1. First preserve existing reconciled usage facts and global totals, attaching
   their resolved execution/parent IDs. Report suspected additional overlap.
2. For subsequent accounting changes, deduplicate only proven repeated request
   facts, add demonstrably disjoint facts, and mark unresolved overlap partial.
   Any changed reconciliation algorithm gets its own version and comparison.

The shared graph answers identity and structural questions. Projections state
whether they measure workspace-owned facts, a complete family, or a filtered
cohort. Ancestor/sibling context is never silently added to workspace cost.
Unknown ownership remains in an explicit unresolved population instead of
being assigned to a convenient root or omitted from global accounting.

Use shared occurrence ownership for event counts and activity spans as well.
Two retained copies of one invocation are one canonical call. Original sources
remain available for inspection and preservation audits.

## 8. Consumers and migration boundaries

| Existing path | Shared result it should consume |
| --- | --- |
| `delegationSnapshot`, incoming ancestry, reader heading/picker | Executions, resolved parents, attempts, labels and order |
| Timeline message and tool projections | Same executions/order plus original occurrence ownership; inherited content can be shown as context |
| `usageConversationRootsWith`, usage cohorts and quick statistics | Execution bindings, parent/root/depth certainty and shared existence rules |
| Conversation activity and concurrency | Original activity occurrences and the same resolved lineage; retain their explicit timing-estimation assumptions |
| Authorship, labeling and agent-event inputs | Shared occurrence provenance and lineage; keep content-specific classification separate |
| Offline labeling snapshots | The same evidence schema and pure resolver, with explicit compatibility coverage |

`agent_sessions` can remain the raw accounting/observation carrier. Creating a
session candidate during ingestion need not assert that an execution happened.
Provider ingestion may normalize evidence but should stop publishing a separate
final graph. During migration, old helpers can be adapters over the shared
result; consumers must not add their old parent/alias fallback afterward.

### Revision and performance contract

Publish immutable results keyed by resolver version and transactional evidence
generation. Resolve and load contributing evidence from a consistent database
snapshot. A partial-family result must use the same rules and agree with a
complete result wherever its ownership is marked resolved; a bound that blocks
that conclusion produces incomplete coverage.

Retain existing cache coalescing, bounded family caches, and snapshot mismatch
responses. Timeline and navigator snapshots capture the execution/occurrence
component digests and transcript/session workspace placement of their
contributing conversations, including context and continuation segments. Their
tokens hash those dependencies with the displayed rows, rather than the
catalog-wide revision. Lane reads recheck the captured dependency signatures
inside their read transaction before using its resolution and occurrence
ownership. Unrelated evidence can advance the global revision without expiring
the view; changed ancestry, missing referents arriving, deletions and copied
message ownership still require a refresh. Workspace regrouping also requires a
refresh: its placement signature is captured and rechecked separately from the
execution component digest because lane queries filter by workspace. The check
also runs when execution identity keeps the same revision, since occurrence
ownership can change alone.
Peer composition retains its common-resolution requirement and combines the
parts' dependency manifests. Projecting a window does not rerun ownership with
only that window's evidence.

Global consumers can scan compact identity evidence once per generation.
Interactive family lookups need indexed adjacency, bounded metadata pages and
lazy event batches. Keep large transcript bodies out of graph caches. Measure
cold/warm cost and memory on the existing approximately 48,000-conversation
catalog before choosing persistent materialization. Also benchmark index
rebuilds and affected-component recomputation during active ingestion: record
ingest throughput, read latency, invalidation fan-out, background backlog and
memory. A cache rebuilt for every append must not indefinitely starve reads or
derivative refresh. Establish budgets from these measurements before rollout;
a cold/warm read benchmark alone does not establish viability.

### Ongoing invalidation and background publication

A one-time derivative rebuild is insufficient. A late acknowledgement in A
can change B's execution binding or inherited-message ownership while B's
transcript and stored `parent_id` remain unchanged. Current
[`labelsStructure` and dirty triggers](../internal/archive/labels.go) do not
encode all of these cross-conversation dependencies.

Persisted consumers must record the resolver version, their resolution
dependency fingerprint, and content/model contract versions. The resolver or
evidence index maintains reverse dependencies from claims, source bindings and
occurrences to affected conversations and derivatives. Include unresolved
identifier lookups and missing-parent/history dependencies, so the appearance
of previously absent evidence can invalidate a result. Deletions and
corrections need the same treatment as new evidence.

On an evidence-generation change:

1. Compute the affected dependency closure across old and new components,
   including identities that merge/split, descendants whose ancestry changes,
   and occurrences whose equivalence or owner changes. If precision is
   unavailable, invalidate the whole affected family; a resolver/extractor
   version change can require a broader rebuild.
2. Invalidate cached projections and mark persisted membership/context, event
   facts, lineage/cohorts and affected aggregates stale before presenting the
   new revision as current. Reads must check derivative freshness against the
   current dependency state, including while propagation/rebuild is pending;
   return pending/partial coverage instead of stale attribution as current.
3. Rebuild through the ordinary background pipeline. At publication, atomically
   verify the captured dependency fingerprint/version is still current and
   clear only the dirty token the job consumed. If it changed, discard the
   stale membership/context publication and retain/requeue the dirty work.

Dirty propagation and freshness checks must survive restart; a pending change
cannot live only in an in-memory queue. A conservative generation check is an
acceptable first implementation, though unrelated ingestion need not force
re-inference once component dependencies can be checked precisely.

Separate inference facts from their derived execution membership and context.
Reuse a content inference result only when its full input hash and model/
contract match, including framing/context fields where the classifier uses
them. An ownership change alone need not rerun identical inference, but must
refresh its attribution. New outputs must never publish against an obsolete
resolution just because the source message text is unchanged.

## 9. Delivery plan and acceptance criteria

1. **Conformance first.** Convert the audit reproducers into shared fixtures
   with expected semantic executions, attempts, parents, occurrence owners,
   certainty, labels, order and conserved accounting facts. Include provider
   inputs that expose ingestion fallbacks and suppression.
2. **Extraction/backfill readiness.** Inventory provider evidence retention,
   add typed provenance/identifier kinds before normalization discards them,
   and produce the affected-record/reindex manifest. Validate extractor/index
   version changes and unavailable-source coverage. Do not promote legacy
   materialized decisions into stronger evidence.
3. **Resolver in shadow mode.** Build resolution and dependency tracking;
   compare old/new results read-only. Validate alias restoration, identity
   conflicts, repeatable backfill, and ingestion-time performance. Expose a
   comparison artifact, not a user-visible switch between definitions.
4. **Migrate structural consumers together.** Move navigator, reader metadata,
   usage lineage and quick statistics to the same bindings. Replace Timeline
   outline-prefix filtering and branch ancestry with parent IDs/ancestor
   closure before retiring outlines. Preserve URLs and saved disclosures.
   Display partial/unknown results consistently while activity migration lands.
5. **Migrate occurrence and persisted consumers.** Use original ownership in
   Timeline, tool counts, activity/concurrency, authorship and labeling inputs. Update
   offline snapshot evidence/versioning. Ship dependency invalidation,
   freshness checks and guarded background publication with each migrated
   derivative. Remove obsolete local resolvers.
6. **Validate and rebuild derivatives.** Review catalog-wide count/parent
   changes, preserve source and accounting evidence, then version/rebuild
   affected derived outputs. Do not ship a supposedly complete fix while
   copied activity still belongs to the child.

Each fixture should run through both the pure resolver and the real consumer
APIs. Cover acknowledged and rejected launches; task failures after creation;
tokenless agents; copied history across compaction; continuations; mirrors;
embedded and mixed-provider agents; missing/competing parents; cycles;
namespace isolation; incomplete families; independent accounting; and late
arrival of stronger identity evidence.

Required additions from design review:

- Provider-input cases distinguish a Claude fallback parent from a verified
  delegation; Cursor copying retains boundaries and suppressed-observation
  evidence. Re-extraction must be repeatable and source-preserving; missing
  historical input leaves uncertainty rather than fabricated confirmation.
- Occurrence cases cover shifted and coincident `line-N` locators, repeated
  identical messages, compaction with/without retained mappings, and missing
  originals with a navigable surviving copy.
- Identity cases contradict verified delegation with both weaker and equally
  strong continuation/mirror claims, including conflicts elsewhere in the
  component. Assert uncertainty and count coverage as well as ancestry.
- Build persisted derivatives, then add stronger evidence in a different
  conversation without changing the child's rows. Verify automatic correction
  without a manual full rebuild. Repeat with a job paused before publication,
  a restart during invalidation, and evidence removal/splits. Unchanged full
  inference inputs remain reusable; stale membership/context cannot publish.
- Restore legacy `delegation_session`, `delegation_collapsed`,
  `delegation_expanded`, and session-derived disclosure keys after an alias
  upgrade. Cover alias collisions, unresolved/split aliases and unrelated keys.
- Timeline filtering retains inactive ancestors of active children using IDs;
  missing/paginated parents have explicit coverage, and branch layout works
  without outlines while preserving common-row order.

Check invariance under evidence loading order, window/filter changes, page
boundaries and reader selection. For the original Peirce case, acceptance is
one execution with one label, one originating launch occurrence, correct
counts in every consumer, and access to both preserved source observations.
For a two-sibling case, both remain available and in the same relative order
after either is opened; no numeric outline expectation is needed.

Keep resolver semantics, ingestion and background publication tests in Go.
Client tree/filter transformations and URL alias normalization belong in Node
unit tests. Browser tests verify navigation, disclosure/selection restoration,
focus and rendering. Do not test exact CSS, label copy, or the continued
presence/absence of a numbered presentation.

## Implementation scope notes

The migration covers the consumers in section 8. Findings currently retains
its stored-parent/depth conversation cohort partition, and Thinking and
compactions retain their existing native-alias representative-fact selection.
Those analysis-specific policies are not canonical execution projections and
are not migrated by this change. In particular, do not interpret their source
cohort or retained-fact counts as the shared resolver's execution cardinality.
A future migration of those surfaces must use the shared resolver for any
semantic root or execution identity it exposes, while preserving their separate
fact-selection and accounting contracts.

## 10. Questions for reviewers

These have recommended defaults above; review them before implementation:

1. Is the proposed evidence precedence and identity-conflict policy sufficient
   for all providers? Does the extraction manifest identify every legacy
   fallback or suppression that requires a provider reread?
2. Can a read-only per-generation evidence index meet cold-start and inverse
   lookup requirements, or should the first implementation persist typed claims?
3. Are source-anchor aliases sufficient for identity upgrades/splits, or do
   durable cross-catalog execution references require an additional registry?
4. Is launch time with stable source-position/anchor fallbacks the right total
   order, particularly for missing timestamps and clock skew?
5. Should unresolved attempts be an inline group or remain only in transcript
   evidence? In either case, confirmed agent counts must stay unambiguous.
6. Does the proposed preserve-first accounting migration adequately handle
   positive usage in both a handle stream and its linked child transcript?
7. Can the dependency manifest cover new evidence, deletion, splits and
   unresolved lookups without excessive rebuilds during active ingestion, and
   do publication guards cover every persisted consumer?

Review against the audit's live and constructed cases. Please distinguish
changes to evidence interpretation from legitimate differences in metric
scope, and avoid introducing another consumer-specific definition of an agent.
