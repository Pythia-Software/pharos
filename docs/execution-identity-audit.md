# Execution identity and hierarchy audit

Audit date: 2026-11-07. Code inspected: `a1143ecb` plus the pending fix for
the copied `explore` launch. This is an audit, not an implementation of the
shared execution model proposed below. This audit records the preimplementation state; the shared resolver implementation and validation are described in [the design](execution-resolver-design.md) and [the evidence manifest](execution-evidence-backfill.md).

Follow-up design: [shared execution resolver](execution-resolver-design.md).
The subsequent product decision removes `1.x` sub-session numbering. Numeric
outline stability in F2 is no longer a remediation requirement; preserving
sub-session visibility, parentage and relative order remains required.

The original problem is one instance of a broader inconsistency. Pharos has
multiple executable definitions of an execution, its parent, and inherited
history. With fixed source records, those definitions can produce different
execution counts, parents, depths, and outlines. This is reproducible without
background indexing, changing source data, or a browser race.

Eight groups of defects were reproduced, plus one policy disagreement that
needs a decision. Three groups were also demonstrated against the user's live
catalog: extra analytics execution identities, navigation renumbering, and
copied activity attributed to the child.

## Scope and evidence

The audit followed provider records through session ingestion, stored child
links, explorer reconciliation, incoming ancestry, Hierarchy and Timeline,
usage attribution, quick statistics, concurrency, and labeling. It inspected
the cache/version boundary as well. Source adapter coverage was targeted to
Codex forks and Claude delegation reconciliation; this was not a complete
conformance audit of every provider or every historical transcript format.

The existing catalog was opened read-only, without initialization, reindexing,
maintenance, or migrations. At the metadata scan it contained:

| Metadata | Count | Meaning |
| --- | ---: | --- |
| Conversations | 48,202 | Catalog size, not a count of audited executions |
| Agent-session records | 57,649 | Includes handles and accounting streams |
| Recorded conversation parent edges | 4,831 | Not all are delegation edges |
| Cross-workspace parent edges | 1,071 | Workspace-local reasoning is insufficient |
| Parents with multiple cross-workspace agent children | 69 | Conditions for the outline issue; not 69 individually verified failures |
| Codex parent edges at recorded depth zero | 10 | Continuation-shaped records; not ten independently confirmed misclassifications |
| Persisted explicit child links | 1,115 | Stronger linkage already available to some consumers |
| Persisted child links to a conversation lacking a transcript parent | 0 | Discovery failure below was demonstrated by fixtures, not this scan |
| Recorded subagents without a conversation parent | 0 | Orphan failure below was demonstrated by fixtures, not this scan |

Twenty-one targeted fixture checks were executed. Fifteen exposed defects,
one exposed the independent-usage policy disagreement, and five positive
controls passed. These were deliberately adversarial cases, not a random
sample or an estimated failure rate. Related failing checks share root causes.

The fixture source and runner are retained locally in
`.context/execution-audit/audit_test.go` and `run.py`; their complete output is
`results.txt`. The read-only catalog probe and output are `live_test.go`,
`run-live.py`, and `live-results.txt` in that directory. Metadata and tool
observations are in `live-metadata.json` and `live-tools.json`. These are
gitignored working evidence, not members of the normal test suite.

## Where the definitions differ

| Layer | Definition and inputs | Consumers |
| --- | --- | --- |
| Session derivation | `agentSessionSummariesWithCostState` creates a candidate for every retained delegation; it operates on one transcript | Persisted `agent_sessions`, membership and accounting streams |
| Claude ingestion | `claudeLineageEdges` validates acknowledgements, assignment corroboration for textual handles, competing claims and cycles; `linkClaudeGroupDelegations` persists accepted zero-token handles | Subsequent readers of `child_conversation_id` |
| Explorer | `resolveDelegationLinks` additionally reads launch results, rejects failed attempts, and now omits some inherited launches; `loadDelegationAncestors` resolves incoming owners | Hierarchy, reader heading/picker, Timeline lane identities |
| Analytics | `usageConversationRootsWith` reads conversation parents, session parents, persisted child links, mirror links and inferred command launches; it does not read launch acknowledgements or the explorer's exclusions | Usage lineage/cohorts, quick statistics, conversation activity, concurrency, origin attribution and labeling depth |
| Inherited content | Token counters subtract inherited usage; labeling matches inherited messages separately; explorer launch exclusion uses another narrower match | Different treatment of the same retained history |

Relevant entry points are [token_usage.go](../internal/archive/token_usage.go),
[claude_lineage.go](../internal/archive/claude_lineage.go),
`delegation_links.go` (retired; now [execution_resolver.go](../internal/archive/execution_resolver.go)),
[usage_conversations.go](../internal/archive/usage_conversations.go), and
[labeling_prepare.go](../internal/archive/labeling_prepare.go).

Preserving multiple source observations is useful. Independently interpreting
them as different execution graphs is the problem. Workspace totals, raw
transcript views and whole-family totals can legitimately have different
scopes; they should still agree about the identity and owner of an execution
present in both scopes.

## Findings

### F1 — P1: analytics counts attempts and transcript copies as executions

**Status: live catalog and executable fixtures.**

`usageConversationRootsWith` does not load acknowledgement or launch-outcome
evidence (`usage_conversations.go:34–56`). A non-main session without a
persisted child link becomes `agent:<session-id>` (`:222–233`). Quick statistics
then visits every resolved session identity, including tokenless ones
(`quick_stats.go:183–198`). Tokenless real agents must count; the missing
distinction is between a real agent, an unexecuted attempt, and another
observation of an existing agent.

The fixtures produced these results:

| Fixed evidence | Explorer | Analytics / quick statistics |
| --- | --- | --- |
| One acknowledged child, no persisted child link | One child execution | Two delegated identities: launch and transcript |
| Same child plus its copied spawn call | One child execution after the pending fix | Three delegated identities |
| Spawn rejected by the thread limit | No child | One delegated run; the root becomes a `parent` instead of a `singleton` |
| A nested child's transcript parent is flattened, but its launch identifies the actual owner | Depth 2 under the child | Depth 1 under the root |

The original conversation confirms the three-identity case. The analytics
resolver assigns one identity to the parent's launch, another to Peirce's main
stream, and a third at depth 2 to the copied launch. The patched explorer
correctly shows one child. The quick-stat algorithm uses those analytics
identities; its complete counting path was also exercised on the fixtures.

Consequences include incorrect delegated-run counts and parent/standalone
classification, plus inconsistent depth and parent fields in usage analyses.
Labeling uses that same analytics depth, so a disagreement can propagate into
depth cohorts. This audit did not measure how many historical labeling units
or aggregate cohorts are affected.

Reproductions: `acknowledged-handle-count`, `copied-launch-count`,
`failed-launch-count`, `nested-acknowledgement-depth`.

### F2 — P1: navigation renumbers an unchanged execution

**Status: live catalog and executable fixture.**

Incoming ancestry deliberately omits siblings in other workspaces. The
explorer subsequently numbers the remaining visible tree from one
(`delegation.go:806–841`). Therefore a second child shown as `1.2` becomes
`1.1` when opened in its own workspace. Identity can change even when the
parent relationship is correct and no copied launch exists.

A live read reproduced `1.2` → `1.1` for
`conversation_f0eeca4b22d05efb89f7190d14cdc90c`. The constructed two-sibling
case reproduces the same behavior. Root ordering can also depend on the
projection because ancestor context lacks the same timing evidence used to
order the complete parent view.

An outline used in reader headings as the session's identity must be derived
before projecting away siblings, or explicitly replaced with an identity
whose meaning does not depend on the visible subtree.

Reproduction: `sibling-outline-on-navigation`.

### F3 — P1: continuation ancestry becomes delegation in the explorer

**Status: executable fixture; continuation-shaped live records exist.**

Analytics treats a Codex conversation with a parent and `agent_depth=0` as a
continuation of the same execution. The explorer attaches its main stream
below the parent's main stream without distinguishing the relationship
(`delegation.go:377–387`), then assigns it another level and counts a child.

The same fixture produces explorer depth 1 and analytics depth 0 with no new
delegated execution. Across workspaces, family discovery follows recorded
children only when `agent_depth>0`, introducing an additional difference in
which side of a continuation can see the relationship.

The underlying conversation parent field represents more than one kind of
relationship. A continuation edge must not silently become a delegation edge.

Reproduction: `continuation-is-same-execution`.

### F4 — P1: removing the phantom row does not remove copied activity

**Status: live catalog and executable fixtures.**

The pending fix omits an inherited launch session from the explorer graph.
It does not classify the copied message or tool observation as inherited.
Tool evidence continues to be attributed through session membership and the
conversation fallback (`delegation.go:477–485,512–535`). Timeline message
projection independently selects retained session messages without an
inherited-origin exclusion (`delegation_labels.go:103–135`). Quick statistics
counts retained tool rows directly.

In the original parent and child, the same spawn has the same native message
ID and call ID in both transcripts. Its timestamps differ by 34 ms. The live
read confirmed that it remains both a message and a tool mark in the child's
Timeline after the phantom row is removed. The child reports 27 self calls,
including that inherited parent spawn. The two transcripts contain 19 and 27
tool rows respectively, with this one shared call ID.

The fixture counts a single copied physical call twice in both explorer
totals and quick statistics. This can distort call counts, recorded tool time
when a duration is retained, and activity bounds. Showing inherited history
as transcript context is reasonable; attributing it to the child's own
execution activity needs a distinct rule.

The new launch exclusion also matches only the direct parent's retained
launch, exact body and zero-token candidate. A three-level fixture in which
the intermediate transcript no longer retains the original launch leaves
the grandparent's copied launch visible as a new execution below the leaf.
The earlier fix is containment for the observed case, not general inherited
history resolution.

This qualifies the earlier statement that Timeline needs no separate change:
its lane structure shares the corrected explorer graph, but its message and
tool occurrence ownership still needs correction.

Reproductions: `copied-tools-and-timeline-history`,
`copied-launch-after-intermediate-compaction`.

### F5 — P1/P2: conflicts and incomplete ancestry receive incompatible certainty

**Status: executable fixtures; not established as live-catalog occurrences.**

Two equally explicit child links from different parents cause analytics to
mark the child unresolved with reason `conflict`. The explorer overwrites the
chosen parent while iterating launch rows (`delegation.go:395–419`); incoming
ancestry takes the last explicit claim (`delegation_links.go:445–461`). The
fixture chooses parent B, labels the relationship exact, and emits no warning.

Two related incomplete-evidence cases also disagree:

- A recorded depth-2 subagent with no available parent becomes an explorer
  root at depth 0 with quality `observed`. Analytics reports `missing_parent`.
- A cyclic pair has one edge removed by the explorer, after which it displays
  observed depths 0 and 1. Analytics leaves both unresolved with reason `cycle`.
  The explorer does emit a cycle warning, but the row depth does not retain
  that uncertainty.

The P1 issue is confident selection of an arbitrary exact parent. The P2 issue
is presenting layout depth as observed execution depth for unresolved graphs.
Conflict, missing ancestry, and cycle status should survive layout and remain
available to every consumer.

Reproductions: `conflicting-exact-parents`, `orphan-depth-is-unknown`,
`cycle-treatment`.

### F6 — P2: family discovery ignores some of its strongest ownership evidence

**Status: executable fixtures; the metadata scan found no persisted child link
without a transcript parent in the live catalog.**

`loadDelegationFamily` follows recorded conversation parents and inferred
command launches (`delegation_links.go:96–105`). It does not follow
`agent_sessions.child_conversation_id`. A persisted exact child link can
therefore provide a direct navigation reference without bringing that child's
descendants into the parent's family view. Opening the child then reveals
the previously omitted descendants.

Acknowledgement matching is also limited to the already-discovered candidate
set. An exact provider/account/native thread ID cannot recover a child in
another workspace if its transcript parent is missing. The child is not in
the candidates, and incoming ancestry does not search acknowledgement-only
owners without a parent hint or persisted declaration.

Stronger evidence is being constrained by discovery through weaker evidence.
Discovery and resolution must share the accepted edge set, with indexed
lookup for native identities rather than an unbounded scan of transcripts.

Reproductions: `exact-edge-without-transcript-parent`,
`declared-edge-descendant-discovery`.

### F7 — P2: report prose can become an exact ownership assertion

**Status: executable fixture; not established as a live-catalog occurrence.**

Claude ingestion accepts a textual launch ID only in a recognizable launch
acknowledgement and corroborates its assignment
(`claude_lineage.go:137–161,203–222`). The explorer's
`readDelegationHandle` searches arbitrary content strings for `agentId:`
(`delegation_links.go:53–57`).

The fixture result contains only “During review I compared agentId: target
with the old logs.” Claude ingestion rejects it as ownership evidence. The
explorer reparents the target under the reporting agent and labels the edge
`exact-delegation-handle`.

Provider-specific acknowledgement parsers should produce one shared typed
evidence record. General agent report text must not establish execution identity.

Reproduction: `report-prose-is-not-a-launch-acknowledgement`.

### F8 — P2: suffix handle matching bypasses provider/account checks

**Status: executable fixture; not established as a live-catalog occurrence.**

The `native == handle` branch checks provider and account. The alternative
`strings.HasSuffix(native, "agent-"+handle)` branch does not
(`delegation_links.go:228–230`). With one matching suffix in the candidate
set, the explorer can merge a launch into a transcript from a different
provider/account. The fixture demonstrated that merge. A direct native-ID
control correctly stayed unmerged across the same boundary.

Identifiers must be resolved in their declared namespace. Legitimate
mixed-provider command launches should continue to use their own explicit
evidence type; they do not justify treating suffixes as globally unique IDs.

Reproductions: `suffix-handle-isolation` and its passing
`provider-account-isolation` control.

## Policy decision: identity and independently reported usage

The explorer retains two source sessions when a linked launch has its own
reported tokens and emits an independent-usage warning. Analytics aliases
the handle into the child execution whenever the explicit child relationship
is present, regardless of that token evidence. The fixture produces three
explorer nodes and a single shared analytics identity for handle and child.

This is a confirmed policy disagreement, not proof that either token total is
wrong. Independent accounting evidence does not, by itself, prove a second
execution; an explicit identity link also does not prove two reported usage
records are duplicates. The shared model needs separate decisions for
execution identity and accounting overlap. Do not repair this by unconditionally
dropping a positive-token stream or blindly adding duplicate reports.

Reproduction: `retained-independent-usage-policy`.

## What the audit did not establish

- No incorrect global token total or invoice-equivalent cost was demonstrated.
  The fork-counter test and token-conservation control passed. Incorrect
  ownership can still redistribute usage among parent/depth/cohort groups
  without changing the global sum.
- The fixture findings do not establish the frequency of conflicts, cycles,
  suffix collisions, or prose-derived links in historical data.
- Labeling and concurrency consume lineage, but the audit did not rebuild
  every derived dataset or assess all existing saved charts and dashboard cards.
- Differing totals caused solely by documented workspace scope, unknown
  external usage, mirror suppression, or omitted ancestor accounting were
  not classified as hierarchy defects.
- The demonstrated failures use fixed evidence and bypass the explorer cache.
  The cache already uses transactional generations and rejects mismatched
  snapshot requests. No cache defect was needed to explain these failures.

## Recommended shared model

Introduce one versioned execution resolver and make the existing surfaces
consume its result. Preserve the raw source objects and their provenance.
Shadow comparison can begin as a read-only derived model over existing
records. Those records are not sufficient for every provenance decision:
historical ingestion can synthesize parents or suppress copied observations.
The follow-up design requires an extraction/backfill inventory and provider
input tests before rollout, preserving uncertainty where source evidence is
unavailable. Persistence is a performance decision after the semantics and
active-ingestion costs are tested.

It should expose:

1. **Execution identity:** stable IDs and mappings from conversation, mirror,
   continuation and launch-handle observations to executions. Identity must
   not depend on which workspace is open, which rows are visible, or which
   source currently supplies a usage ledger.
2. **Typed relationships:** delegation, continuation, mirror, and inferred
   command launch are distinct. Execution ownership has a common precedence
   and explicit `confirmed`, `inferred`, `ambiguous`, `missing`, or `cycle`
   resolution state, with the supporting observations retained.
3. **Occurrence ownership:** message/tool/request observations identify their
   original execution and whether they are inherited copies, original events,
   or unresolved duplicates. Transcript context can retain copies while
   activity and accounting projections avoid asserting another execution/event.
4. **Accounting membership:** usage evidence is reconciled separately from
   structural identity, retaining unknown and overlapping reports for review.
5. **Stable family presentation:** headings, labels, sibling order and URL session
   mappings are chosen from resolved identity before projection. Pagination,
   collapsed siblings and incoming context must not reorder common executions
   or implicitly hide legitimate sessions on reader navigation. Stable numeric
   outlines are not required after the subsequent decision to remove numbering.

Keep workspace-local accounting and whole-family structure as separate
projections of that model. Keep bounds and cached compact family metadata;
there is no need to fetch every transcript to render a workspace. If a bound
prevents resolution, return a coverage/uncertainty state instead of silently
switching to a different hierarchy definition.

Suggested implementation order:

The follow-up design's delivery plan supersedes this initial sequence. It adds
an extraction/backfill gate, explicit replacement of outline-based Timeline
ancestry, and ongoing derivative invalidation with guarded publication.

1. Establish a permanent shared conformance fixture suite from these cases.
   Assert semantic identity, parent and certainty across all consumer APIs.
2. Extract provider acknowledgement evidence and implement execution/edge
   resolution once. Preserve old session and conversation URLs through alias
   mappings. Resolve the independent-usage policy before migrating it.
3. Move explorer, incoming ancestry, usage lineage and quick statistics onto
   the shared result. Remove their independent ownership decisions.
4. Apply occurrence ownership to Timeline, tool counts, concurrency inputs,
   and labeling inputs/snapshots. Reuse the same resolved depth and evidence.
5. Compare old/new derived outputs on the live catalog read-only, separating
   corrected counts from legitimate scope changes. Version and rebuild derived
   products only after that review; retain source records and accounting facts.

Required conformance cases include failed versus task-failed launches,
acknowledged handles, missing acknowledgements, copied ancestral history,
compaction, continuations, mirrors, embedded agents, cross-workspace and
mixed-provider chains, conflicting claims, missing parents, cycles,
provider/account isolation, independent accounting, and sibling ordering.
Also verify navigation round trips, collapsed/paginated views and token
conservation against the same fixtures. Browser tests should exercise
navigation/focus and rendering; execution semantics belong in Go tests.

## Verification and changes made

The selected existing delegation, usage ownership, session-role, fork-token,
quick-stat and parallelism tests passed. `go test -short ./...` passed, and
`git diff --check` passed before this report was added. No frontend code or
generated bundles were changed for the audit.

The diagnostic fixture runner intentionally exits nonzero for the reproduced
violations. It copies a temporary test into `internal/archive`, runs the
checks, captures output, and removes the temporary file. To repeat locally:

```sh
python3 .context/execution-audit/run.py
python3 .context/execution-audit/run-live.py
```

The live runner opens the existing catalog read-only. Its catalog location is
local to this workspace's audit evidence. Do not run either temporary runner
concurrently with another copy of itself.

This report is the only new repository artifact from the audit. The prior
copied-launch fix and its regression tests remain pending separately. No
additional production remediation, catalog repair, publication, or data
migration was performed.
