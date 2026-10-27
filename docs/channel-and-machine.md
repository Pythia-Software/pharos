# Channel and machine as core dimensions

Implementation and design, 2026-10-26.

## Implemented behavior

Channel, Machine, and Execution machine are available as columns, filters, and
grouping fields alongside Provider in usage, Library, writing, tool calls,
tool summaries, skills, conversation activity, and parallelism. Token, tool,
activity, and carbon charts expose the applicable breakdowns; the Card Library
includes token and API-cost cards for all three dimensions. Writing and Library
also expose membership combinations for work containing several origins.

The field keys are `channel`, `machine_id`, and `execution_machine_id`. Machine
IDs remain in saved queries; readable names come from the machine registry.
`machine` and `execution_machine` hold display names. Conversation details
include attribution statuses and evidence. The HTTP metadata endpoint is
`/api/origins`. MCP statistics, conversation discovery, message search, and
text search accept the corresponding filters; statistics can group conversation
entities by provider, channel, and machine together. Workspace statistics use
`channels` and `machines` membership combinations to avoid multiplying totals.

Source observations survive copy-authority changes. Root resolution shares the
usage lineage machinery, including mirror identities, native parents, exact
delegation handles, and inferred shell launches. Materialized attribution is
invalidated by graph/evidence changes; affected descendant workspaces invalidate
their tool-summary partitions. Read-only MCP opens resolve the same metadata
from a read snapshot without acquiring the catalog writer.

Native host records now retain an opaque hardware-derived machine ID separately
from the OS-user-specific capture host. Historical native sightings supply
inferred machine placement only when all observed hosts have mappings and agree.
Exports do not acquire their download device. New canonical producers can declare
initiation and execution placement explicitly; see [canonical export](canonical-export.md).
Normal incremental sync reparses existing sources under updated extractor
versions to retain initiation metadata, including the first native entrypoint
when a conversation is resumed through another runtime.

Execution placement currently has session granularity. Ambiguous placement stays
unknown or conflicted; sources do not yet provide the telemetry needed to divide
a single migrating execution into device-specific time segments. The design
below describes that extension as well as the implemented contract.

## Recommendation

Add **Channel** and **Machine** beside Provider throughout Pharos. Channel answers
“through what did this work start?” Machine answers “where did it start?” Every
descendant inherits its initiating conversation's channel, including descendants
started through another provider, another app, or a shell command.

Keep the session's own provider and harness. A Codex Desktop conversation that
launches Claude through a shell has `channel=desktop` throughout its tree, while
the child still has `provider=claude` and its recorded Claude harness. This lets
us ask both “how much work started in Desktop?” and “which agents did that work
use?”

Keep both **Initiation machine** and **Execution machine**, with the former
labeled Machine in ordinary breakdowns. This is the confirmed product decision:
Machine defaults to where the task started and is inherited by descendants;
Execution machine identifies where each session runs. Keeping both preserves
the starting device and the actual execution location when delegation crosses
devices. Here execution means the device running the agent harness or tools,
not the provider's model inference servers.

## What is already available

These concepts overlap existing fields without being equivalent to them:

| Existing concept | What it currently means | Implication |
| --- | --- | --- |
| `provider` | The recorded agent/product, such as Codex or Claude | Preserve its current semantics; this change does not require a provider taxonomy migration. |
| `source_kind` | The adapter/source through which a workspace is indexed | A native Codex transcript can represent work started in Conductor or TL1. Source remains useful for archive provenance. |
| `harness` and harness versions | Recorded launcher/runtime details; native Codex uses `codex/<originator>`, Claude can use `claude-code/<entrypoint>` | Useful evidence for channel, but a delegated CLI harness does not change the initiating channel. Some adapters supply wrapper or generic harness labels. |
| `conversations.parent_id`, session parents, exact delegation handles, `conversation_launches` | Relationships between executions, including across workspaces/providers | The basis for inherited attribution. |
| Conversation identity links | Mirrored representations of the same conversation | Combine their attribution evidence even when one transcript supplies the usage totals. |
| `hosts`, sightings, captures | Where Pharos saw source data; one host is a Mac **and OS user** | Evidence about data location, not a physical-device dimension by itself. |
| `conversations.origin_host_id` | The host supplying the authoritative archived copy; can become `merged` | Must not be renamed or directly exposed as the initiating/execution machine. |

The relevant implementation is in [adapters.go](../internal/archive/adapters.go),
[adapter_conductor.go](../internal/archive/adapter_conductor.go),
[adapter_tl1.go](../internal/archive/adapter_tl1.go),
[harness_version.go](../internal/archive/harness_version.go),
[usage_conversations.go](../internal/archive/usage_conversations.go),
[delegation_links.go](../internal/archive/delegation_links.go),
[launches.go](../internal/archive/launches.go),
[hosts.go](../internal/archive/hosts.go), and
[copies.go](../internal/archive/copies.go).

`usageConversationRoots` already resolves roots across mirrors, embedded
agents, native parents, exact handles, command launches, and filename
continuations. Missing parents and cycles stay unresolved. Reuse and extract
that machinery into shared attribution rather than implementing independent
channel inheritance in every dataset. Its current representative-dependent
parent selection needs an explicit conflict policy before becoming the common
attribution contract.

## Channel vocabulary and evidence

Use stable provider-independent keys, with readable labels:

| Key | Label | Meaning |
| --- | --- | --- |
| `conductor` | Conductor | Work initiated through Conductor. |
| `tl1` | TL1 | Work initiated through TL1. |
| `cli` | CLI | Work initiated directly through a command-line client. |
| `desktop` | Desktop app | Work initiated through a provider's standalone desktop app. |
| `web` | Web | Work initiated through a browser interface. |
| `ide` | IDE | Work initiated through an editor integration. |
| `api` | API / integration | Work with positive evidence of initiation through an API integration. |
| null | Unknown channel | The archive cannot establish how the work started. |

Conductor is more specific than Desktop app; it gets its own channel. Codex and
Claude can both appear under CLI or Desktop app. Preserve the specific app,
raw originator, and entrypoint in evidence so a finer breakdown can be added
without reparsing everything. Do not encode provider into channel values.

Resolve a root's channel from evidence about **its initiation**, in this order:

1. An explicit, versioned initiation declaration from the source producer or
   canonical export, or an explicit user correction tied to that conversation.
2. An exact orchestration record proving that Conductor or TL1 created the
   run. A mirror's wrapper can supply this evidence even when native usage wins
   deduplication. A wrapper that merely imports or resumes a preexisting run is
   not proof of initiation through that wrapper.
3. A documented native initiation marker, through a versioned mapping of
   observed `originator`, `source`, and `entrypoint` values.
4. Unknown.

An explicit correction should retain the evidence it supersedes. Incompatible
claims of equal authority produce a conflict, not whichever source indexed
last. An SDK entrypoint or headless process by itself proves a runtime, not
who started it. A bare `source_kind=codex` or `harness=codex` does not prove CLI.
New unrecognized markers remain unknown until their meaning is established.
Do not infer the channel from transcript prose mentioning an app or a familiar
working-directory path.

The native parser currently saves Codex originator in workspace metadata and
the harness, but its `session_meta` events do not become ordinary retained
message records. Preserve the full attribution-relevant metadata explicitly at
ingest. Backfill from metadata, exact wrapper records, and captured headers
where available; missing original headers must remain an honest coverage gap.

The ChatGPT adapter currently records `chatgpt-export` and `user-export`.
An export downloaded on a Mac does not establish whether the conversation
started in Web, Desktop, or Mobile. Keep its channel unknown unless the export
contains supporting metadata. Likewise, `canonical` and `tl1-export` describe
transport; inspect explicit producer metadata before assigning a channel.

## Inheritance and the unit of initiation

Resolve attribution over the complete recorded graph before applying a time
window, provider filter, repository filter, or search. A child active this week
can inherit from a root last active months ago or stored in another workspace.

The resolution order is:

1. Normalize exact mirror identities and collect the evidence from all copies.
2. Resolve causal parents using recorded ancestry and exact delegation handles;
   use accepted command-launch inference only when stronger evidence is absent.
   Preserve edge evidence and conflicts. Match provider/account-scoped native
   identifiers, not a bare session ID across arbitrary sources or devices.
3. Resolve the initiating root, with cycle and missing-parent detection.
4. Resolve initiation evidence on that root and assign its channel to every
   descendant and to its token, tool, writing, skill, and activity facts.
5. Resolve execution-machine evidence separately for each run or time segment.

Native ancestry, exact handles, and inferred shell launches have different
evidence quality. An accepted inferred link participates in inheritance but
remains inspectable as inferred. Do not weaken the existing launch matcher's
device restrictions to attach arbitrary remote runs; remote delegation needs
an explicit causal link or equivalent strong evidence.

If a known child has no resolvable parent, its own CLI marker must not turn it
into CLI-initiated usage. Its channel stays unknown, with `missing_parent` as
the reason. Similarly, a resolved root with unknown channel passes unknown to
all descendants, even if their local harnesses are recognized. Keep their
observed runtime metadata for diagnosis.

A later parent or mirror can resolve previously unknown attribution. A later
causal link can also correct a run provisionally treated as a root; lack of a
recorded parent does not prove human initiation. Recompute descendants when
the graph changes. Historical attribution can improve as coverage improves;
saved filters refer to the current evidence, while paged query snapshots keep
the attribution captured when they were created.

Continuations of the same execution keep the original initiation. Resuming a
CLI-created conversation in Desktop does not move its historical usage to
Desktop under this definition. A genuine new human-created fork is a new
initiation; do not treat a content/ancestry link alone as delegation. Preserve
the existing distinction between filename continuations and delegated runs.

This first version attributes a conversation tree to where that tree began.
It does not answer “which app did I use for each later prompt?” If that becomes
important, add interaction-level channel/device facts; do not silently redefine
the origin dimension.

| Recorded scenario | Channel on root and descendants | Machine (where the task started) |
| --- | --- | --- |
| Codex in Conductor delegates to Codex | Conductor | Root's initiating device. |
| Codex Desktop launches Claude with a shell command | Desktop app | Desktop's initiating device, including the Claude usage. |
| Direct terminal task delegates to a remote worker | CLI | Initiating terminal device; remote device is Execution machine. |
| Desktop agent launches a TL1 workflow, with a proven causal link | Desktop app | Desktop's initiating device; TL1 remains recorded as the downstream orchestrator/source. |
| Independently scheduled TL1 workflow | TL1 | The recorded initiation environment for that workflow; scheduling it previously from Desktop does not establish a parent session. |
| Native Codex transcript mirrored in Conductor | Conductor when creation is proven | One originating device; usage counted once. |
| Child transcript arrives before its Desktop parent | Unknown until resolved, then Desktop app | Unknown initiation machine until established. |
| ChatGPT export without client metadata, downloaded on another Mac | Unknown | Unknown; downloading does not relocate the conversation. |

## Machine identity and Web/cloud coverage

Add a machine registry separate from `hosts`. Physical machine IDs should survive
renames and OS-account changes; retain host IDs for capture/configuration
isolation. Multiple host accounts on one device can map to one machine only
when there is reliable identity evidence. An opaque hardware-derived ID can
support newly observed Macs without exporting a raw hardware UUID.

Existing host IDs hash hardware identity **and username**; their original
hardware component cannot be recovered from the hash. Old labels and usernames
are insufficient for merging machines. Backfill mappings from available device
records or an explicit user mapping, otherwise retain unresolved identity.

Represent an origin as one of:

- A known physical device, with a stable key and editable label such as
  “MacBook Pro”.
- A recorded web environment with no identified client device, displayed as
  “Web (device unknown)”.
- A recorded cloud environment with a stable environment key when available,
  otherwise “Cloud (environment unknown)”.
- Unknown machine.

Web is a useful fallback location category, not a fabricated physical device.
If metadata identifies a browser task's actual device, group it under that
device and keep `channel=web`. A web origin with no device should not collapse
into generic unknown. Export contents that do not even prove Web stay unknown.
Keep whether the location is a device, an environment, or a coarse fallback
explicit in the registry and API. Coarse buckets do not count as known devices.

Local transcript location, capture-host identity, and wrapper installation are
useful **inferred** machine evidence when the source contract establishes local
execution. Synced/copied transcripts, downloaded exports, remote app backends,
and imported canonical data do not establish execution on the ingesting Mac.
Machine evidence must be independent of copy authority: indexing a newer copy
or carrying the library to another Mac must not move its usage. Do not choose
the first or latest sighting as the originating device merely because it is
available.

Initiation-machine inheritance follows the same causal tree as channel;
execution-machine attribution does not inherit across a remote launch. If the
same run can migrate between devices, preserve time-bounded execution segments
and attribute facts to their segment. When the archive cannot locate an event
within a segment, retain unknown execution placement instead of duplicating
its usage across sightings or assigning the entire conversation to its latest
device. Root initiation remains stable across subsequent resumes.

The existing “Machine Tokens” and “Machine Time” names refer to agent work;
they do not already supply device identity. Tool calls' existing `host` field
is a URL hostname. Neither is a field to repurpose for this dimension.

## A common attribution model

Persist observations separately from resolved attribution. A suitable initial
shape is:

| Proposed storage | Purpose |
| --- | --- |
| `machines` and `host_machines` | Stable device/environment identity, labels, kind/precision, and evidenced mappings from capture hosts. |
| Attribution observations keyed by conversation/run and evidence locator | Native initiation markers, wrapper creation declarations, channel and machine claims, execution intervals, evidence type, source host, and extractor version. Multiple mirrors can contribute. |
| A conversation-origin projection keyed by canonical conversation | Initiating root ID, resolved channel, initiation machine, per-field status/evidence references, graph revision, resolver version. |
| Execution placement keyed by execution and optional interval | The device/environment running that execution. A child transcript and its parent's delegation handle refer to the same execution. |

Treat this as logical storage, not a requirement to add one large provenance
framework before shipping. Keep a compact projection for SQL joins and shared
Go accessors for row producers. The source observations must survive projection
rebuilds and copy-authority changes.

On conversation and fact datasets expose:

- `channel`: effective inherited initiating channel.
- `machine_id`: effective inherited initiation location; label the column
  Machine and resolve its display label through metadata.
- `machine_kind`: physical device, web/cloud environment, or coarse fallback.
- `execution_machine_id` and its kind when execution placement is available.
- Attribution detail fields: initiating root, per-field status and evidence.
  Suggested statuses distinguish direct, inherited, inferred, unknown,
  missing parent, conflict, and cycle. Preserve both the origin-evidence quality
  and the inheritance path; “inherited” alone does not establish certainty.

Keep machine IDs in saved queries, filters, chart selections, and exported
facts. Labels are presentation, so renaming a device cannot break saved views.
Channel keys are likewise stable. Unknown stays null in data and gets an
explicit label in UI; a literal “Unknown” string must not become a real app or
device. Known coarse environments have their own stable bucket keys.

At workspace grain expose `channels` and `machines` as distinct membership
sets, like `providers`, with scalar values only when the member conversations
share one value. Mixed and partially unknown membership must remain visible.
Do not assign a workspace its first conversation's origin and then attribute
all of its metrics to that origin.

For additive channel/machine charts, aggregate facts after attribution. Do not
explode a workspace's total once per member channel/device. Workspace-membership
filters mean “work containing matching conversations”; a whole-workspace metric
under that filter remains a whole-workspace metric and must be labeled as such.
Fact filters mean “only matching facts.” Publish both meanings explicitly where
the product currently mixes workspace discovery and usage analysis. Grouping
workspace rows by a membership combination can retain current Providers-style
semantics, but cannot be presented as additive per-channel spend.

## Support across Pharos

“Core dimension” means more than adding columns to the token table:

| Surface | Required support |
| --- | --- |
| Machine Tokens / usage | Columns, filter values, grouping, chart splits, presets, summaries under the same filters, and linked details. Preserve a child's own provider/model alongside its initiating channel/machine. |
| Library and work details | Channel/machine membership columns and filters, conversation-level values, execution location, and inspectable attribution evidence. |
| Human Words and writing messages | Event/conversation attribution before workspace aggregation; equivalent chart splits, filters, and drill-downs. Count each human message once. |
| Tools, Tool calls, and Machine Time | Raw-call fields and SQL filters, cube and daily-rollup dimensions, chart scopes, and navigation. Summary and raw-call destinations must agree. |
| Skills/instructions | The owning conversation/execution's attribution in usage rows, filters, and grouped views. |
| Parallelism and conversation activity | Initiating channel/machine on root-managed units and events. Cross-device occupancy needs a defined device scope; label Library-wide concurrency separately from device-local concurrency. A channel filter must not silently change how the occupancy level itself was computed. |
| Carbon | Same inherited dimensions and filters as the underlying usage; this is attributed estimated provider energy, not measurements of the initiating Mac's power. |
| Home and Card Library | Discoverable channel/machine cards and generic dimensions in recipe exploration, covers, chart labels, and destinations. |
| Search, reader, delegation explorer | Channel/machine filters wherever provider filters exist; show inherited origin and actual execution details. A matched child keeps its parent's origin even when the parent is outside the search. |
| MCP / CLI / HTTP | Channel/machine filters, metadata selection, supported grouping, discovery of known values, evidence/coverage, and consistent snapshot handling. |
| TL1 and findings | Add the dimensions where provider or per-session breakdowns are meaningful. TL1-rooted cohorts normally share TL1; retain observed downstream orchestrator details when their origin is another channel. A finding spanning several roots uses sets, not a fabricated scalar. |
| Saved views, links, and exports | Stable keys, validated round trips, shared filter semantics, label resolution, and additive canonical-export metadata for future producers. |

The JSON schemas in `schemas/` are the field contracts. Existing provider fields
occur in usage, tools, tool calls, writing, writing messages, skills,
parallelism, conversation activity, and Library. Update those contracts, their
generated Go schemas, and the corresponding row producers together.

Schemas alone do not cover the UI. For example,
[usage-presets.ts](../web/src/usage-presets.ts),
[usage-chart.tsx](../web/src/usage-chart.tsx),
[tools-dashboard-model.ts](../web/src/tools-dashboard-model.ts),
[machine-activity.tsx](../web/src/machine-activity.tsx),
[carbon-impact.tsx](../web/src/carbon-impact.tsx),
[card-exploration.ts](../web/src/card-exploration.ts), and the recipe/chart helpers
have explicit dimension lists or provider-specific labels. Introduce one small
frontend dimension registry for field, label, unknown label, value labeling,
chart eligibility, and navigation instead of adding another independent switch
at every call site. Dataset schemas still control which fields actually exist.

MCP is a separate contract:
[mcp_statistics_schema.go](../internal/archive/mcp_statistics_schema.go) currently
allows grouping only by repository/source, and
[mcp_metrics.go](../internal/archive/mcp_metrics.go) restricts metadata by entity
grain. Extend these intentionally; adding Query Table fields does not make them
available to agents. Preserve workspace versus conversation metric scope and
freeze attribution and labels with metric snapshots.

Ordinary field/schema integration should use published Query Table facilities.
If membership filtering or machine-ID display requires an unavailable upstream
capability, write an upstream handoff and consume its release. No local package,
module-cache, or generated-bundle patches.

## Rebuilds, performance, and historical migration

Root attribution should be a shared materialized projection, rather than a
whole-catalog traversal per table/chart request. Keep usage queries that do not
need origin fields cheap; the existing `usageNeedsLineage` optimization is a
useful precedent. Index resolved channel, machine, and root keys where actual
query plans need them.

The tools cube and daily summary currently discard conversation identity while
grouping. Add origin/execution dimensions **before** that aggregation; attaching
a channel afterward cannot recover a mixed group's allocation. A changed
root/alias invalidates affected descendant workspaces and their rollup
partitions, not just the root's own workspace. Rebuild daily summaries from
the attributed cube. Measure the extra grouping cardinality.

Add the new inputs/projections to query-cache generation and the relevant
Library, MCP metric, tool-rollup, activity, and card-cache invalidation/version
paths. Machine label changes refresh metadata and rendered labels. A device
identity correction or a causal-link correction can reassign facts and requires
projection/rollup invalidation. Existing `origin_host_id` changes are treated as
bookkeeping by `query_cache_generation.go`; another reason not to reuse it.

Backfill in bounded, resumable batches using existing records and captures
before asking for source rescans. Prioritize exact Conductor/TL1 creation
evidence, native initiation markers, and known host-to-machine mappings. Record
resolver/extractor versions, pending work, and coverage. Do not block every
startup on a full transcript scan.

Unknown attribution must remain in unfiltered totals and chart denominators.
Show unknown/conflicting placement by both session count and token/cost volume;
one large unresolved task can matter more than many small resolved sessions.
Do not claim known-channel coverage proves complete capture or complete causal
lineage. For datasets that already exclude unresolved roots, retain their
documented population policy and report exclusions separately.

Adding these dimensions must not change unfiltered token, cost, tool-call,
human-message, or human-word totals. Existing source/provider saved views keep
their meanings. New dimension filters must affect summaries, tables, charts,
and drill-down destinations consistently.

## Suggested delivery sequence

1. **Attribution foundation:** establish the channel vocabulary, retain source
   observations, add machine identity with initiation as the default breakdown,
   and share the existing lineage resolver. Expose evidence and missing/conflict
   states on conversation details before using the dimensions for comparisons.
2. **First complete path:** backfill, then ship usage plus Library columns,
   filters, chart splits, saved links, and MCP conversation metadata from that
   same attribution projection. Demonstrate Desktop-to-CLI delegation and
   native/wrapper deduplication end to end.
3. **Complete parity:** writing, tools/cubes, skills, activity/parallelism,
   carbon, cards, search, applicable findings/TL1 views, and remaining MCP/CLI
   contracts. Treat this as required completion of the feature rather than
   leaving “core” dimensions available only on Tokens.
4. **Improve coverage:** add mappings and producer/export contracts for devices,
   web/cloud origins, and remote causal links that historical data cannot prove.
   Interaction-level attribution is a separate future capability.

## Acceptance cases

Extend existing Go/Node tests where the behavior lives. The essential cases are:

- Multi-level, mixed-provider delegation and shell launches inherit the root
  channel, including when children have explicit CLI runtime metadata.
- A descendant active in the selected window inherits from an older root;
  provider/repository/search filters do not change that root.
- Child-before-parent and wrapper-before/after-native ingest produce the same
  final attribution and totals. A suppressed mirror can supply origin evidence.
- Missing parents, unknown roots, cycles, and conflicting claims remain visible
  and do not fall back to the child's channel or an arbitrary machine.
- Resumes, continuations, independent new forks, and exact delegation handles
  preserve their distinct identity and causality semantics.
- Copy authority changes, off-host captures, library moves, renamed devices,
  multiple OS accounts, and downloaded exports do not move historical origins
  or duplicate usage.
- Remote delegation has one inherited initiation machine and a separately
  recorded execution machine; migration segments allocate each fact once.
- Mixed workspaces and partially unknown membership preserve fact totals;
  cube/daily totals equal raw-call totals for every supported dimension/filter.
- Late root/identity corrections invalidate all affected descendant projections
  and rollups, including across workspaces; saved snapshot pages stay stable.
- UI/query/MCP contracts round-trip known IDs, unknowns, and coarse environments;
  renamed labels keep working with saved filters. Chart totals, table metrics,
  and drill-down destinations agree.

Browser tests should cover rendering, layout, and focus only. Inheritance,
allocation, filters, URLs, and dimension-registry behavior belong in Go/Node.
