# Population based work execution plan

Status: implementation under review in a four-PR stack, with classic execution still the default; this design lands with the first PR's contracts and measurement harness. The [implementation and acceptance record](work-execution-acceptance.md) tracks observed evidence and the gates required before activation. Design baseline: November 7, 2026, commit `cb2ee633ff2c2afd789dd0cd981c8033731bea77`; the retained comparison baseline is now main commit `289ad3aa1c1f40dfc0c215dcf77c61eff0241beb` at catalog schema 24.

## 1 Purpose and proposed direction

Replace the separate scheduling decisions around indexing, analysis, maintenance, and model backlogs with a small execution system. Its primary request is **bring a specified population current**. It selects bounded work, respects real data dependencies, shares an explicit resource allowance, and records enough facts to explain execution without reconstructing it from goroutines and logs.

The integrated release includes a shared CPU allowance that stages receive explicitly, resource history, and a consistent request/execution model. An internal producer-to-consumer proof must demonstrate “bring my last two days of activity current before processing the historical backlog” before the wider conversion is accepted; it is not a separate partial production rollout. Independent downstream work should become eligible when its upstream inputs for that population are committed. Whole-library prerequisites remain whole-library prerequisites when correctness requires them. A CPU limit alone does not deliver this priority behavior.

This is an execution refactor. Preserve the analysis algorithms, model assets, canonical records, derived output formats, retention rules, and existing data-safety protocols. Changing calculation semantics at the same time would make correctness comparisons and rollback much harder.

The design must meet six requirements:

1. Execution is simple to describe and debug. There is one scheduling policy and an explicit owner for each queue and budget.
2. Existing and new binaries can safely alternate against the same library during the evaluation period, preserving work committed by either.
3. Realistic paired measurements establish both total throughput and the time until prioritized data is usable.
4. A reproducible 20% increase in full-rebuild duration or resource consumption is unacceptable. Smaller regressions also require explanation; 20% is a rejection boundary, not a target.
5. Adding, disabling, invalidating, or backfilling an analysis becomes easier. The replacement should remove bespoke scheduling code after the comparison period.
6. Resource behavior becomes observable over time, both for the Pharos process tree and for individual tasks where attribution is defensible. Live history, task summaries, and optional profiles should identify opportunities to reduce CPU/memory/I/O and increase useful parallelism without confusing an allowance with actual usage.

Being pre-users favors a cohesive replacement developed in four stacked PRs, followed by one integrated production cutover after correctness, performance, and rollback gates pass. The stack divides review and rollback boundaries, not four product launches. Keep intermediate commits buildable and the new execution path dormant in lower PRs; do not spend time making every partly converted subset a supported production mode. Preserve a short-lived execution switch for comparison and a known compatible previous binary for rollback. Remove the comparison path no later than the release immediately following integrated acceptance; a failed gate postpones acceptance rather than extending duplicate coordination indefinitely. Removing it also sets the task inventory's `comparison_window` to `closed`. The inventory's schema records the retained artifact; corpus admission in the experiment harness enforces compatibility without coupling ordinary tests to future schema migrations.

### 1.1 Design choices after review

| Question | Decision and reason |
| --- | --- |
| How is the work delivered? | Four stacked PRs: contract/proofs/harness, executor/resources, domain conversion, and integrated UI/cutover/acceptance. One cohesive release follows the complete stack; internal proof order is not a sequence of production experiments. |
| Should CPU plumbing be built early? | Yes, as explicit allocation passed to stages, initially per process, in the shared executor/resource PR. It does not need its own production release or make a broker rewrite a prerequisite. |
| Is recent-first scheduling already solved? | Some local ordering exists. The missing contract is recent activity progressing through all required stages before unrelated history; measure it and prove the selected producer-to-consumer path internally before accepting the complete conversion. |
| Does disposable scheduler state need its own database? | No. Keep queues, request selections, and scheduling decisions in memory. Existing domain dirty markers and recovery checkpoints preserve necessary work. |
| Should the executor depend on a large performance campaign? | No. Iterate on a representative relationally valid slice, use stable work/resource counters, and reserve full-corpus paired runs for substantial release gates. |
| Should consolidation depend entirely on proving a recent-readiness speedup? | No. Uniform allocation, explanations, disablement, and backfills remain maintenance goals. Measurements determine which population boundaries and concurrency changes earn their complexity. |
| Is a task-kind dependency graph still useful? | Yes, as a small static table of real prerequisites with domain-owned readiness checks. Do not turn it into a database of per-record jobs or a new general workflow framework. |
| Should resource insight be part of the refactor? | Yes. The integrated release includes bounded aggregate history, task identity, available measurements, profiling access, and honest attribution. Do not make perfect task-level heap/GPU accounting a prerequisite or quietly drop the monitoring goal. |

Robustness means explicit ownership, dependency discovery, request/cancellation semantics, stable resource configurations, bounded failures, and demonstrated recovery. It does not mean a larger framework or every possible future optimization.

The goal is to replace duplicated orchestration, not merely relocate every existing loop behind an interface. A task keeps its algorithm, dirty-state logic, and safe publication protocol; the common runner owns ordering, admission, cancellation, and status.

## 2 Existing implementation and useful foundations

The following are current behavior, not promises made by the proposed scheduler:

| Area | Current implementation | Consequence for the replacement |
| --- | --- | --- |
| Operation identity and status | `OperationSpec`, `Op`, and the kinds registry describe work, parents, holds, affected freshness domains, priority, and stop behavior. Registration mostly observes execution; individual paths own their locks and scheduling. | Extend these identities and status surfaces. Do not introduce a second public operation tree. |
| Main rebuild | Full recovery captures, indexes captures in order, runs deferred analysis stages in order, and verifies. Retained repair has its own workspace loop. | Preserve current ordering first; separate coordination from batch execution deliberately. |
| Scheduled work | Background items have cadences, enablement, power policies, and a sequential pass. Automatic indexing has its own admission loop. | Cadence should create eligible demand; a shared executor should decide when the demand runs. |
| Maintenance | Projection and search maintenance have separate loops and existing batch functions. Some maintenance is held during ingestion. | Reuse existing batch boundaries. Do not remove ingestion guards until narrower publication boundaries are proven safe. |
| Local models | Labels and agent events do a bounded initial share and can launch independent backlog runs. They share a model manager and inference budget; preparation has separate worker pools. | Replace backlog launch policy, preserving preparation, framing, inference, and saving behavior. Account for preparation and inference together. |
| Authorship | Frequent writing estimates already have bounded processing, a pending queue, dispositions, and provisional results. Exact refinement uses corpus relationships, chronological state, templates, mirrors, and launch history. | Preserve the distinction between estimates and exact refinement. Exact results cannot be obtained by running isolated conversation classifiers. |
| Tools rollup | Dirty workspaces and full invalidation coexist. Construction uses a read snapshot and temporary tables; publication checks concurrent changes. | Partitioning exists, but a scheduler cannot simply split and reorder its SQL. Keep the existing construction/publication contract initially. |
| Correctness state | Dirty revisions, generations, checkpoints, and freshness markers are already maintained by domain code. | These remain authoritative. Queue completion alone cannot establish freshness. |
| Historical measurements | Stage histories contain useful durations and work counts. Sync CPU/allocation measurements include concurrent process work; they are not exclusive task costs. | Retain useful history but attach its real scope/basis. It cannot substitute for controlled comparisons or be relabeled as per-task attribution. |
| Resource display | The CPU/memory/I/O sampler reads processes/descendants on status requests, with a 1.5-second minimum interval. GPU driver-client time is also request-driven, shared for five seconds. There is no retained rolling resource history or general task attribution. The native app is currently outside that total. | Add one bounded history collector per observed process scope, independent of an open page, and task instrumentation at execution boundaries. Preserve partial/unavailable semantics and disclose process coverage. |
| Compatibility | The catalog has a schema-version guard; the retained baseline supports version 24 and refuses newer versions. | A scheduler-only release must not increase the catalog compatibility floor. Test the exact retained baseline binary. |

Implementation references are collected in section 16. The classification below is intentionally conservative where partition correctness has not been established.

## 3 Simplicity rules

The execution system should fit the following explanation:

> Select the highest-priority eligible population. Find its first ready batch in the declared analysis order. Acquire the capacity it needs. Run it to its next safe boundary. Commit outputs and correctness state. Reconsider the queue.

Independent batches may run concurrently when their declared inputs, existing locks, and resource allowances permit it. Deterministic selection order does not imply deterministic completion order.

Keep these limits on the implementation:

- A static dependency graph of task kinds, validated for cycles at startup and in tests. No persisted graph node or dependency edge per message.
- A small set of correctness scopes: source group, conversation, workspace/repository, and library. An analysis may use a more specific existing partition only when its contract documents it.
- One ordinary batch entry point per task kind. Long-lived model sessions and algorithm state belong to a task implementation, not to an elaborate workflow language.
- One state vocabulary, reusing the existing operation states. Policy exclusion and incomplete data remain explicit reasons, not additional state machines.
- One owner of a batch at a time. Retry committed work safely when necessary; do not build distributed exactly-once execution.
- A bounded in-memory ready window. Reuse durable domain checkpoints; do not add a scheduler state file. Never materialize the entire message corpus as scheduler jobs or put transcript text into queue metadata.
- No scheduler learning, work stealing, speculative downstream execution, or utilization feedback controller in the initial design.
- No permanent old/new scheduler stack. The comparison seam has an explicit removal phase.

Do not use a dependency declaration as a substitute for a real lock. The existing catalog writer, source/capture locks, library lifecycle, custody, backup, and eject protocols continue to enforce their safety responsibilities.

### 3.1 Minimal execution interface

The positive design is one ordinary loop over a small task table. This sketch describes responsibilities; it is not a commitment to final type names or a new plugin framework:

```go
type Allocation struct {
    CPUs int // total CPU worker parallelism available to this invocation
}

type Task interface {
    Next(context.Context, Population) (Selection, error) // cheap bounded lookup only
    RunBatch(context.Context, Selection, Allocation) (BatchResult, error)
}
```

The static task table associates an existing operation kind with this adapter and its real prerequisites. `Next` is a cheap bounded lookup over a prepared inventory and indexed readiness metadata. It returns the next admitted phase/selection or a concrete blocker; it does not scan the library, create corpus context, compute dependency closure, or durably mark a large population dirty. A required discovery/preparation pass is itself a `RunBatch` selection under the same CPU/memory/I/O admission, cancellation, and attribution contract. The owning domain still decides and commits invalidation. The adapter must support the selected population; calling a global `RunLabels` loop and naming its first results a population batch is not this contract.

`RunBatch` executes an admitted discovery, preparation, compute, or publication step and returns committed progress, whether relevant pending work remains, and any concrete blocker. These are phases of the same domain task, not a new graph of micro-jobs. Expensive request-membership construction follows the same admission rule. Before returning it joins its CPU workers and finishes or safely records its publication; it cannot leave a hidden inference/preparation tail running after its grant is released. A reusable session may retain idle model/context state, with memory charged, but starts no further compute until the next admitted invocation. There is no new durable task cursor: resumability comes from the domain's existing dirty tokens, hashes, and recovery state. Preserve native domain units rather than defining a universal row type. A dependency declaration references the consumer's required scope and quality, not every implementation table it touches.

The runner cheaply chooses a ready phase, obtains its allocation before substantial work begins, passes it as an argument, and releases it after the safe boundary. It also attaches operation/task/batch identity to the context and records resource ownership boundaries so diagnostics use the same identities as execution. Stage code uses `allocation.CPUs` for its Go pools and native worker thread settings instead of reading `NumCPU` or acquiring a second hidden budget. Serial stages use one CPU; an I/O wait does not invent extra CPU entitlement. Model/GPU residency continues through the existing manager, with memory and device accounting described in section 7.

A lower-stack adapter can accept the same allocation while still being exercised by today's coordinator in comparison tests. This is the same execution contract used by the later runner, not a separate permanent CPU-control system. Reuse registry metadata and sensible defaults for simple tasks; adding a serial independent task should not require eight new configuration objects.

## 4 Task classification

Every registered structural operation must receive one reviewed classification. Compound operations are request/coordination objects; their leaf work receives resource allowances. Existing operation holds describe safety obligations and must not be interpreted as saying every task reserves a catalog writer for its whole lifetime.

### 4.1 Analysis and maintenance classes

| Work family and existing kinds | Correctness scope and dependencies | Initial execution batch | Resource and priority treatment |
| --- | --- | --- | --- |
| Source discovery and capture: `capture.this-mac`, `capture.cli`, `capture.history-backfill`, capture portions of updates | Source inventory, acquisition, native snapshots, and existing capture locks. A database snapshot is not a set of independently captured conversations. | Existing file/source groups and copy/snapshot boundaries. Keep indivisible native snapshot steps intact. | Mostly disk I/O, some compression/parsing CPU. Preserve capture guarantees; do not recapture a source once per population. |
| Ingestion and retained repair: `index.changes.*`, `index.captures`, `index.sources.legacy`, `ingest.cli`, `library.rebuild` | A provider's existing source group or a workspace. Parents/subagents, mirrors, and source-only metadata can enlarge the scope. Requires accepted evidence. | Existing ingestion unit/workspace commit, bounded by bytes or messages where supported. Do not split a transactional group just to hit a timer. | CPU plus short writer turns and source/index locks. Discovery must precede prioritization when timestamps are unknown. |
| Retained-input verification: `index.verify` and rebuild verification stages | Accepted source evidence, its existing comparison scope, and any required repair admission. Audit success is separate from analysis freshness. | Existing evidence/group audit unit with its byte limits and durable pending revision. | Parsing CPU, disk reads, and writer turns only for recorded outcomes or authorized repairs. Preserve resumability and mismatch reporting. |
| Repository identities: `analysis.github`, `analysis.identities` | Identity and mirror relationships can span workspaces. Repository-forge checks may need external access. | Existing reconciliation pass initially; batch lookups only at supported repository boundaries. | Network/I/O and CPU. Global identity publication is a barrier until narrower behavior is demonstrated. |
| Git integration: `analysis.git` | Repository, including workspaces sharing its Git directory. Requires applicable identity/repository inputs. | Existing repository group with shared Git caches; bounded publication. | I/O/subprocess capacity. Avoid repeating history discovery for each conversation. |
| Tool facts and rollups: `tools.ledger.build`, `analysis.tools` | Workspace dirty partitions plus identity/origin context; some invalidations require a whole-library rebuild. | Existing ledger groups and rollup construction/publication first. Later partition only through its generation-safe interface. | CPU, temporary storage, and writer turns. An unsplittable SQL statement is reported explicitly. |
| Frequent writing estimates: `analysis.authorship` | Existing message revision/disposition queue plus mirror identity. Estimates and refined results have different readiness. | Existing bounded queue lanes and publication path, retaining their source hashes and revisions. | CPU and short writer turns. Reuse existing recent/history semantics rather than adding a competing queue. |
| Exact writing refinement: `analysis.authorship.refine` | Corpus relationships, template counts, chronology, launches, and mirror representatives. An append can reuse a validated existing checkpoint; historical changes can require full replay. | Existing exact pass at first. Its internal pages and publication chunks remain progress/safe-stop boundaries, not independently ready conversation outputs. | Library-level dependency and potentially large memory/CPU reservation. Clearly explain when recent work waits for this prerequisite. |
| Message labels: `analysis.labels` | A complete framing unit for a conversation, relevant writing outputs, model identity, and any reuse/source relationships. | Ordered conversation preparation feeding token/length-packed inference batches and bounded result commits. | Shared CPU preparation/inference allowance, model memory, and GPU admission if applicable. Backlog becomes ordinary queued demand. |
| Agent events and label query facts: `analysis.agent_events`, `maintenance.labels.query` | Conversation input/model identity for inference; relevant committed labels for query facts. Cross-conversation dependencies must be declared where actually used. | Existing conversation preparation and inference packing; existing query-fact dirty units. | Same shared model limits. Do not infer a mandatory labels-to-events dependency solely from their current stage order. |
| Fix grouping and titles | Grouping can relate events across conversations; title generation has its own inputs and execution path. | Existing grouping/publication pass initially. Model/API work retains its own applicable policy. | Conservatively a global/grouping barrier. Inventory its current callers, including work launched after events. |
| Findings: `analysis.findings`, including measurement and review | Enabled findings, their data/time windows, and existing feature partitions. Global ranking or comparison may require all relevant inputs. | Existing finding/feature partitions where independent; whole pass otherwise. Preserve the distinction between measurement and full review. | CPU/read-heavy with bounded publication. Never call a whole-library finding current because only recent rows were analyzed. |
| Origins, Search view, previews: `maintenance.origins`, `maintenance.library-view`, `maintenance.previews` | Origins may depend on global lineage/identity; view and preview units have narrower dirty sets after those prerequisites. | Existing origins pass; existing workspace/preview batches. | CPU/I/O and writer turns. Narrower visibility during rebuild requires removal of the current broad ingestion hold through a tested boundary. |
| Stored activity: `maintenance.parallelism` | Per-conversation timing derivation; aggregates also use identity and conversation relationships. | Existing revision-guarded dirty conversation batch. | CPU and writer turns. Conversation timing readiness does not certify every downstream aggregate. |
| Search index builds: `maintenance.search.*` | Each index's existing document/record inventory and index version. Legacy cleanup can be a migration rather than ordinary derived work. | Existing bounded index functions and cursor. | CPU/disk/writer turns. Preserve version/readiness markers and migration semantics. |
| Suggestions, warming, and measurement: `ask.suggestions`, `maintenance.cache-warm`, `maintenance.upgrade-count`, `drive.measure`, `ask.models` | Relevant committed summaries, cache generations, or host/drive metadata. | Existing bounded request/pass; cache work can be abandoned and recomputed. | Normally low priority. External model calls remain subject to their existing settings; the scheduler creates no new calls by itself. |
| Capture compression (specialized file-replacement protocol): `maintenance.capture-compress` | Existing immutable/cold capture eligibility and file/capture safety rules. | Existing file boundary. | CPU/disk; history priority. Stop only where file replacement remains safe. |
| Catalog checkpoints and small persistence: `maintenance.checkpoint`, `maintenance.mcp-history`, `ask.save`, `cloud.reconcile` | Durability, request persistence, or reconciliation state. | Existing short transaction/checkpoint. | Essential maintenance may run during recent-first work. Bound it; never block durability just to satisfy recency ordering. |

### 4.2 Operations with specialized safety protocols

`backup.local`, `cloud.connect`, `cloud.upload`, `cloud.verify`, `cloud.restore`, `library.transfer`, `library.recover`, `library.compact`, `library.deliver`, `library.deliver.capture`, `library.adopt`, `library.create`, `host.add`, `catalog.initialize`, `library.upgrade`, `repositories.merge`, `analysis.agent_events.import`, `service.start`, `library.release`, `labels.fetch`, and `app.update` need explicit coverage in the inventory.

These are not all population analyses. Keep their recovery formats, exclusive locks, lifecycle ownership, and side-effect boundaries. Admit their expensive leaf work through the resource authority where feasible, and expose their blockers through the same operation status. Startup, safe release, and durable finalization must not depend on waiting for the ordinary analysis queue. Scheduler work must not incidentally change catalog migrations, cloud protocols, or release signing.

`catalog.repair` remains specialized: its retained-repair protocol owns the correctness boundary even though its work resembles ingestion. Capture compression likewise preserves its file-replacement protocol; it is not an index/cache readiness task.

Compound kinds such as `analysis.chain`, `analysis.refresh`, `library.update`, `background.capture`, and `background.update` become plans over classified leaf work. They retain user-visible identity and cancellation ownership but consume no CPU allowance while merely waiting for children.

Read-only queries, read-only MCP calls, browser work, and ordinary Ask interactions remain outside the background job queue. Existing interactive-query admission remains authoritative. A read that initiates structural derived work must submit that work through the shared path, with a truthful pending response or the existing synchronous response contract. It must not secretly run a second rebuild.

Before implementation, export the complete kinds registry and enumerate launch sites to catch work not listed here. Each entry must be a population task, coordinator, specialized operation, essential maintenance, or interactive/read-only path. Each registered kind's class is a typed field of its row in the kinds registry (`operation_kinds.go`); the task inventory holds only each class's contract. Unknown structural work fails tests. No “legacy” classification without a named replacement phase and owner.

### 4.3 Worked tasks and the first scope and quality table

Before implementing the runner, complete a task × population × accepted-quality table for the first pipeline and its user surfaces. Separate actual consumer behavior from a proposed stronger readiness requirement. A global barrier is justified by an algorithm's inputs, not by an illustrative diagram.

| Consumer or result | Initial scope and accepted quality | What remains to establish |
| --- | --- | --- |
| Stored conversation timing | Committed conversation events and its dirty revision; complete derivation under the existing timing algorithm, whose occupancy estimates still come from transcript timestamps. | Support a selected set of dirty conversations and maintain existing retention behavior. No authorship prerequisite is needed for this derivation. |
| Message labels | Stored authorship spans, conversation/lineage/mirror/reuse context, model/input hashes. `RunLabels` tracks `authorshipEstimateGenerations`; the builder reads the stored ledger without filtering on refined quality. | Formalize per-unit usable-input readiness, including missing/stale spans and suppressed/ineligible dispositions. Preserve current estimate-compatible behavior; do not add an exact-refinement requirement by accident. |
| Agent-event candidates | The existing event candidate/framing contract and model identity, including any inherited preparation inputs. | Audit the actual candidate builder. Do not add a labels dependency simply because the stages currently run in that order. |
| Exact writing results | The existing validated append state or a complete relational replay when required. | Define which surfaces ask for exact results versus estimates. Recent-first cannot bypass a genuine library-wide prerequisite. |
| Search view and recent-work surfaces | Committed ingestion units plus the origins/identity/projection inputs each surface actually uses. | Prove visibility at a safe unit boundary before narrowing the current whole-ingest hold. |

**Stored activity from selection to restart.** After admitted membership/discovery preparation, the adapter selects a bounded page from `parallelism_dirty`, first intersecting it with the immutable requested membership. It retains the current timing algorithm and reads event inputs and revision consistently. With one CPU it derives serially; additional workers are used only if partitioned reads are safe and measurements justify them. It publishes through `publishParallelism`, whose revision-conditional deletion preserves a newer dirty mark. After commit the runner can schedule a consumer for the completed units. If the process exits before reporting completion, the next process queries domain state again; no scheduler checkpoint is required. The existing timing-retention rule still applies, and conversation timing completion does not certify a global aggregate.

**Labels from selection to restart.** A recent request selects conversations by qualifying activity, including older conversations with new messages, and expands only the framing/reuse/lineage context those conversations require. The adapter checks usable writing inputs for that set. Pending writing work requests the corresponding estimate-compatible producer; an explicitly exact consumer requests refinement instead. It must not block ordinary labels on all historical refinement merely because a global writing task exists.

The label session loads catalog-wide immutable preparation context once per compatible generation under an admitted preparation phase, and reuses the existing model manager. Full scans of structures, dirty tokens, label counts, and dependency closure are budgeted/cancellable work, not hidden in `Next`. Each invocation selects a bounded preparation/inference/commit window. An allocation of four CPUs could use three native inference threads and one preparation worker concurrently; it could instead prepare then infer with all four serially. It may never allocate four preparation workers plus four inference threads under that four-CPU grant. GPU inference still budgets its CPU preparation and existing device/memory limits. This division is fixed at a safe boundary, not continuously autotuned.

Preserve transcript order and token packing inside the selected window. Labels are published with their model, input hash, structure, and dirty token through the existing writer protocol. A cut conversation remains pending. Finishing recent conversations enables applicable selected query-fact/projection work; it does not set the whole-library labels generation. Exact writing updates can dirty those labels again through the current triggers and reuse propagation.

Do not call `newLabelsItemBuilder` or rescan every stored label for each tiny batch. Move compatible run state into a reusable bounded session, discard it when its inputs change, and charge its retained memory. At a priority change, stop filling the historical preparation window, finish/drain the bounded in-flight work, and select recent units next. After restart, reconstruct from `message_labels_dirty`, stored input/model/structure records, and existing source/reuse checks. The old binary understands the same output and unfinished work.

These two adapters are the initial proof of the abstraction. If implementing them needs another queue database, a task-specific scheduler, or frequent whole-corpus rebuilding, simplify the design before expanding it.

## 5 Dependencies and readiness

### 5.1 Declare real inputs

A task definition specifies which committed inputs it consumes, at what scope, and at what quality. Preserve existing sequencing until an edge can be justified or removed with evidence. Current order is not proof that a dependency exists, and a shared dataset is not proof that two tasks can run independently.

Examples of dependency forms:

- **Same unit:** derived timing for conversation A needs the committed messages for A.
- **Expanded scope:** Git work for a recent conversation may require repository context shared with historical workspaces.
- **Global context:** exact pasted-text classification needs a compatible corpus/chronological state before its output can be treated as exact.
- **Aggregate:** a global finding cannot be final until the inputs in its entire declared comparison window are ready.

Do not create a single unqualified `authorship_ready` boolean. Existing estimate, refined, suppressed, ineligible, and deferred meanings must remain distinguishable. A consumer declares which it accepts; it does not silently accept estimates because exact results are expensive.

### 5.2 Completion is a data claim

The identity of useful completion is approximately:

`task kind + algorithm/model version + correctness unit + input revision set + output quality`

Use existing dirty tokens, hashes, and generation identities rather than computing a hash of the entire library at each dispatch. A coarse global generation remains the conservative fallback when narrower invalidation cannot be proven.

The batch publishes output and acknowledges its captured dirty revision through the existing transactional contract. Where the contract supports it, publish unit completion in that same transaction. If inputs changed meanwhile, newer dirty work survives; the old batch cannot clear it. Marking a unit complete in scheduler metadata never overrides a domain's conflicting dirty state.

Global freshness is published only when its full required population is covered. It must never inherit the completion of a recent subset. Partial and provisional output remains visible only under the existing or explicitly documented partial-output contract.

Where a domain has no durable per-unit completion evidence, retain its coarse readiness boundary initially. Add scoped readiness only when it can be validated from committed output provenance and authoritative input revisions. An in-memory cursor or completed callback is not sufficient evidence. This restriction is especially important before allowing downstream execution after a crash or an intervening classic writer.

### 5.3 Resolve dependencies without a message graph

Keep the static task-kind graph small. A readiness function maps a requested population to the units and prerequisite scopes each task actually needs. Use existing dirty inventories and a bounded page of identities. Keep selection and explanation state in memory; use existing domain checkpoints for recovery.

Start with one declared order among ready task kinds. If a recent downstream task is blocked by a historical/global prerequisite, promote the necessary prerequisite to the request's priority and name that expansion. When a prerequisite is disabled or failed, record the resulting blocked/skipped scope. Do not override an explicit disable or silently expand a request into a different calculation.

### 5.4 Durable dependency discovery before population selection

Some existing orchestration establishes necessary pending work; it is not merely an ordering loop. Preserve that responsibility with the owning domain before extracting bounded selectors. The task-kind graph describes execution prerequisites, while domain invalidation records which outputs must be rebuilt. It must not become a second invalidation authority.

For labels, `labelsWork` discovers changed structure, missing outputs, forks, and reuse relationships. It gives dependent conversations their own dirty marks before processing their source, because clearing the source token could otherwise erase the only reason to revisit them. The current explicit `Conversations` path does not perform all the same durable dependent marking; do not assume that passing a selected list into today's whole-run API implements a safe population adapter.

The safe sequence is: admit domain discovery/preparation, discover the affected closure and persist its necessary dependent dirty state, then cheaply select requested executable units and admit computation/publication that acknowledges only the captured revisions. A historical dependent outside the recent population can stay low priority; it cannot disappear from pending work. Existing global/model-version invalidation must likewise remain discoverable after an interrupted selected run. Reuse the domain's broad metadata discovery once per compatible context when necessary, rather than rescanning it for every small batch.

The first correctness proof uses a recent source with a historical fork and reuse dependent outside the request. Execute the source through publication, interrupt immediately, discard all scheduler memory, and reopen with the retained old binary. Every affected dependent must still be discoverable through existing domain state and eventually match a full reference run. Repeat the proof with interruption before invalidation commit, after dependent marking, and after source output commit, plus a new source revision arriving during publication. This proof is an early stack gate, not something deferred until production testing.

## 6 Population selection and batching

### 6.1 Define the request once

A request holds library and host, requested analyses, selection predicate/time bounds, the actual membership-freeze time, input provenance, quality requirement, trigger, priority, and a stable identity in memory. Closed membership and coverage use the centrally bounded compact inventory in section 6.9; a date predicate alone is not frozen membership. Existing operation/recovery records retain the information their current contracts support; the scheduler adds no new durability guarantee for request priority.

For “last two days,” use an explicit UTC interval ending at the request's selection time and the product's canonical activity timestamp. Select conversations/groups containing qualifying activity, even when the conversation began years earlier. Record why a unit belongs. Missing/unreliable timestamps stay in an explained discovery/unclassified population; they must not disappear from all-work completion.

Discovery may have to read an entire source inventory before recent work can be identified. Ingestion may have to read a whole parent/subagent group or conversation to update its recent portion. Report the selected population separately from the additional prerequisite work.

The request's initial membership is finite, but that alone does not guarantee finite execution under continuous edits. New activity creates or coalesces a subsequent live request; it does not move the first request's endpoint indefinitely. A selected unit's target revision, replacement budget, and completion outcome follow section 6.7. Completing a finite request and having no later changes pending are separate facts.

An input generation token is not itself a historical database snapshot. If live data changes, execute against a consistent captured revision and report that revision, or supersede/replan the affected batch. Do not claim to reproduce an old selection-time state that the storage never retained.

### 6.2 Priority belongs to remaining work

Use a small explicit ordering for executable batches, not a priority inherited forever from the parent operation:

1. Safety/durability finalization and minimum service work.
2. Explicit recent/scoped foreground work and only the prerequisites needed for that work. The recent portion of a user-started full rebuild belongs here too.
3. Live incremental recent work.
4. Eligible historical catch-up/backfills, including the historical remainder of a user-started rebuild.
5. Optional warming/disposable work.

A rebuild does not keep its historical batches at foreground priority just because a user clicked Begin. A later explicit recent request outranks its remaining history at the next safe boundary. Choose oldest request first only after classifying the actual remaining work into the same priority class; then use stable task and unit ordering. Shared work receives the highest effective priority of its current compatible demanders, without promoting unrelated units or all future work in that task kind.

For an explicit recent-first request, unrelated historical analyses do not take new batches until it completes, is cancelled, or cannot proceed because of a concrete blocker. Required global work is promoted and named; disabled/missing/failed dependencies are explained and do not prevent unrelated runnable work forever. Essential maintenance and interactive reads remain available. Reconsider blockers on the events in section 6.8 rather than a tight retry loop.

Within a population, bring ready downstream outputs current before advancing unrelated history. After committed ingestion for population A, its estimates, labels, events, and supported projections can proceed as their real prerequisites allow; historical population B need not finish first. Existing ingestion locks must first be narrowed safely, never bypassed by a declaration.

Outside a finite explicit recent-first request, reserve one historical batch after a fixed amount of automatic live-service time, initially proposed at 60 seconds. This prevents a continuous live feed from starving history. Keep authored chronological order inside relational algorithms. Measure the fairness constant, but settle these priority/ownership semantics before tuning it.

### 6.3 Recent work is a pipeline policy

Per-stage newest-first ordering is useful but insufficient. A label backfill can process a recent conversation and then spend a long time on historical conversations while the recent event/projection stage waits behind the entire label run. Sorting by conversation start time also misorders new activity in an old conversation. Neither is fixed by a CPU semaphore.

For an ordinary catch-up or global rebuild, use two initial populations: a finite recent activity set, proposed at the last 48 hours, and the remaining eligible history. Share this selection across the converted stages. Process ready work for the recent set through all requested consumers before admitting unrelated historical batches. Exact refinement/global prerequisites, safety work, and ingestion-discovery requirements remain explicit exceptions. After the recent set settles, drain history; genuinely new arrivals enter the live lane with the fairness rule above.

For example, after ingestion of recent population A commits, the runner updates its usable writing estimates, labels and event candidates, and applicable query facts/projections as their actual prerequisites allow. Historical population B does not need to finish first. If A's projection waits behind the current whole-ingest guard, the runner reports that blocker; only a tested narrowing of that guard enables the intended pipeline. The internal proof should expose this limitation; the integrated cutover must either narrow that guard safely or explicitly classify the affected surface as waiting for the wider prerequisite. It must not claim early Search readiness that its actual publication boundary cannot provide.

Batch size bounds the delay when a recent request arrives during history. Merely finishing a bounded initial share and launching an uninterruptible historical tail recreates the problem. Existing backlogs become ordinary low-priority demand under the same selector, not independent loops that retain the model or CPU indefinitely.

The preparation and proof work in PR 1 measures where recent work waits today in ordinary catch-up, a model/version backfill, and a full rebuild: upstream algorithm, unrelated history, ingestion guard, model capacity, discovery, or publication. Report required recent units and expanded prerequisite units/work separately. This determines the first internal proof and safe conversion boundaries; it does not turn a local ordering fix into a substitute for the cohesive execution system.

### 6.4 Batch construction

Expose a task's existing natural unit and page through dirty units using stable keys, not large offsets or a full rescan on each dispatch. Start from existing tested batch counts and token budgets. Add byte/message limits for heavy-tailed workloads. Count completed committed units, not units merely dispatched.

Use a soft execution target, initially proposed at roughly 0.5–2 seconds for ordinary CPU/SQL batches, plus a hard bound on items/bytes held in memory. This is a tuning hypothesis, not a deadline that interrupts an unsafe transaction. Model batches keep their efficient token packing; an oversized unit or indivisible SQL/native snapshot may exceed the target and must expose its actual safe boundary.

Do not split a conversation's model framing arbitrarily; existing context, deduplication, message ordering, and reuse rules determine safe subdivision. Do not restart model processes, rebuild corpus indexes, or redo repository discovery for every batch. Keep a bounded preparation window, cache reusable immutable context by its real version, and keep model sessions warm within the shared memory policy.

If an existing algorithm has no cheap resumable compute boundary, initially admit it as one clearly labeled coarse task. Its output writes may still be chunked, but that alone does not make its computation preemptible or its output independently ready. Improve that algorithm separately only if measurements identify a priority or throughput problem worth solving.

### 6.5 Relational work

Exact authorship is the key counterexample to universal conversation batching:

1. Validate/reuse the current exact checkpoint when the existing algorithm permits an append.
2. For historical changes, build the required corpus context once and preserve its chronological processing rules.
3. Use existing bounded output/state writes and invalidation-before-publication rules.
4. Make exact readiness visible only at the correctness boundary the algorithm supports.

If a recent request needs this exact result and the context is invalid, show “recent writing refinement waits for the library-wide classification pass.” If the request accepts current estimate semantics, it may finish those estimates while refinement remains pending. The scheduler must not invent a new approximation or silently redefine “up to date.”

The same conservative rule applies to identity reconciliation, global findings, fix grouping, and rollup publication. Conversation membership is a prioritization input, not a proof of computational independence.

### 6.6 Requests express demand for shared execution

A request records what a caller wants; a batch is the actual owned execution. Two requests can demand the same compatible unit/revision/algorithm/quality or global prerequisite. Run it once and let both observe the result. Keep a small in-memory set of demanders for active/planned batches, and derive queued demand from request predicates and domain state rather than storing a graph edge per message.

Sharing requires domain-validated compatible inputs and quality. An exact result does not automatically substitute for every estimate-version output, and two different revisions of a unit must not compute concurrently simply because their work keys differ. Preserve the domain's existing single-owner/publication locks.

The existing operation tree still has one parent per execution, but a requester is not the parent/root of shared execution. Give each independently live task run an executor-owned top-level operation using its registered kind; its admitted batches/helpers are descendants of that execution root. Requests are separate operations that reference the shared `work_id`/`needed_by` identities. Origin/trigger metadata can record who caused it without making requester retention its lifetime owner. Derive execution contexts from the executor/service lifetime and actual demand/hold policy, not the first requester's cancellation context. Count CPU/output progress once; each request counts its own coverage. One request may finish while shared execution continues for another.

| Scenario | Defined behavior |
| --- | --- |
| Two requests overlap | Coalesce only compatible work. Retain each request's finite scope and quality; compute once and report each request's coverage. |
| Cancel one request | Remove that request's demand. Keep work needed by another request or eligible automatic policy; explain that shared execution continues. Cancel the execution at its safe boundary only when no remaining demand needs it. |
| Cancel the last explicit request while automatic catch-up still needs it | The request becomes cancelled; automatic demand can continue at its own priority. Request cancellation is not a durable task-kind pause. |
| Stop the running task itself | Stop/drain the shared execution and place a manual hold on that kind/scope so immediate dirty rediscovery cannot restart it. Show all affected demanders and leave the domain dirty. |
| Resume after a task hold | Explicit Resume/Run now clears the hold. The initial runtime hold ends on service restart, which the UI states; durable pause uses the existing saved background policy. |
| Stop all ordinary background work | Apply the existing library pause/stop policy and drain safely. Safety/finalization work remains allowed. Cancelling one request cannot cancel unrelated requests by traversing its operation children blindly. |
| Restart | Reconstruct necessary work from domain pending state and existing durable policy/recovery jobs. In-memory sharing and runtime holds disappear; lost explicit request intent follows section 8.1. |

These are API semantics to map onto the existing controls, not a proposal for a separate button for every row. A shared task's Stop action must name the actual execution and explain its affected requests; a request's Cancel action detaches that request. Scheduler ownership and cancellation contexts must implement this distinction rather than inheriting cancellation from whichever requester happened to start first.

The lifetime rule applies to registry retention, UI filtering, journaling, leases, stop routing, and lifecycle/eject checks. Current `pruneLocked` can retire a terminal root and recursively forget its descendants; current operation views can hide an old terminal root. Active shared work must therefore remain under its independently live executor root, never under a cancelled request awaiting pruning. Do not finish that execution root until its active descendants have stopped/drained. Add a defensive registry invariant/test that an active descendant cannot be recursively forgotten, including legacy/mis-parented paths; keep the implementation small rather than adding a second registry.

Prove the rule by starting shared work for R1/R2, cancelling R1, advancing the fake clock beyond terminal retention, and forcing the terminal-count/journal pruning thresholds. After R1 is gone, the execution must still appear in activity/inspection, accept Stop, retain correct resource/lease ownership, and participate in release/eject verdicts. Complete it for R2 and only then allow normal retirement. Also test a request disappearing from the UI independently of registry pruning.

### 6.7 Finite coverage under changing inputs

Record the finite selected units, required qualities, and observed target revisions in the admitted compact inventory defined in section 6.9. The requested result is coverage at validated per-unit publication points, not an invented atomic historical snapshot of the whole library. Report the actual revisions/commit times covered and whether newer work remains pending.

Once all required consumers for a unit are valid at a consistent publication boundary, seal that unit's coverage for this request. A later edit creates new live demand and does not reopen every older request. Domain freshness may become stale again immediately; the UI must distinguish completed request coverage from current library freshness.

If a target revision disappears or changes before valid coverage can be published, allow one replacement attempt against a newly observed consistent revision. This is a proposed fixed limit of two attempts for that unit in the finite request, not two retries on every invocation forever. Recheck the complete required consumer set for the replacement; do not combine outputs from incompatible revisions. Where a domain supports a genuine retained snapshot, it may instead finish the original target without chasing edits.

If repeated supersession prevents valid coverage, end the finite request as `interrupted` with reason `inputs-keep-changing`, its committed/unfinished counts, and the affected units. Keep their latest revisions pending for ordinary live processing. Do not report full success, clear dirty work, silently change the requested time window, or continue an unbounded chase. A fixed membership therefore has bounded revision chasing; external dependencies can still require an explicit wait/cancel decision, which is separately visible.

The two-attempt constant can be tuned from the replay workloads without changing the contract: newer changes stay pending, covered requests do not reopen indefinitely, and unsuccessful coverage has an honest terminal outcome.

### 6.8 Bounded failures and blocker reconsideration

A repeatedly failing first unit must not monopolize the selector. The adapter classifies a failure as a transient execution error, permanent/configuration/input error, or a dependency/capacity/policy blocker. Domain-specific inference retries/splitting remain with that domain; the scheduler must not multiply them with an independent retry loop.

For transient errors without an existing domain retry owner, propose at most three total attempts for a given unit revision, with delays of 5 and 30 seconds. Store the retry deadline/error in bounded memory and choose other runnable units during backoff. After exhaustion, fail that demand/record the blocked unit while leaving authoritative pending work intact. Retry only after an input/config/model change or explicit user retry, not on every background tick.

Configuration, missing-asset, and unsupported-input failures wait for their relevant setting/asset/input change or explicit retry. Capacity/lock/power blockers reconsider when capacity is released, a lock owner completes, policy/power changes, or the existing bounded reconciliation tick runs. Pure waits do not consume execution retry attempts. Do not poll a blocker with a new goroutine or scan per unit.

Keep failed-unit details bounded, with aggregate counts and representative identities. If the bounded skip/diagnostic set fills, hold the affected task kind with a clear failure summary rather than forgetting failures and spinning. A process restart may rediscover dirty work and try again; persistent correctness still resides in the domain, not a new retry database.

Test overlap/cancel/stop, continuous edits, and a permanently failing first unit as concrete scenarios before broad conversion. Their completion and resource ownership should be explainable from one inspect response.

### 6.9 Bounded finite request inventories

Use an explicit compact in-memory membership inventory. Do not infer a frozen set by repeatedly evaluating a timestamp predicate or an ID watermark unsupported by storage. A historical insert or edit after freeze can satisfy that predicate while belonging to new live demand rather than the original request.

Admission constructs the inventory from one consistent metadata read of canonical natural unit identities, their relevant target revisions, and the requested time/scope predicate. Store `requested_at`/time bounds separately from `membership_frozen_at`: if memory/admission delayed discovery, the snapshot taken at actual freeze is the membership contract, not a claim that the library was snapshotted when the user clicked. IDs whose activity later moves outside the filter stay in the closed inventory; newly inserted matching units enter a later live request. Deletions remain named members until the domain resolves their output-removal/retirement outcome.

For capture/ingestion whose conversation identities are not known yet, first freeze the accepted source-group/evidence inventory using the existing consistent capture protocol. Derived identities are enrolled only from those recorded inputs as admitted discovery processes them; membership is marked `discovering` until that inventory closes. Work from later independent ingestion is not silently added. This is a finite input inventory plus bounded discovery, not a changing date query. Record the source/evidence and closure provenance; where storage cannot support a trustworthy frozen input set, expose that coarse boundary or reject the unsupported finite-scope promise.

The inventory representation is centrally defined:

- Store a sorted compact slice of canonical unit identities and opaque target-revision references, containing only metadata needed for scope/readiness. Use ordinary Go slices/structs and measured string storage; no custom global interning allocator or reference-counting subsystem. Do not use an unqualified SQLite rowid that can change or be reused across deletion/rebuild.
- Slice position is the dense ordinal for coverage arrays. Exact-identical closed inventories may share an immutable object through ordinary Go references; overlapping but different snapshots have distinct inventories and are charged at their actual full size. Do not require a deduplication service to admit one request.
- Per-request/per-required-task coverage, sealed state, and the two-attempt replacement count use packed arrays/bitsets indexed by inventory ordinal. Variable replacement revisions/diagnostic details use a bounded sparse table. Do not allocate a Go object, dependency edge, or full output hash list per message just to track request progress.
- Inventory units follow real domain scope: conversation, source group, workspace/repository, or a declared coarse library unit. A task's internal message/document batches remain in its existing domain queue; this is not a second full corpus of message jobs. A domain needing finer inventory must demonstrate its bounded representation.

Proposed initial admission limits are 32 MiB per finite request, 128 MiB of inventory/bookkeeping per process, and eight admitted explicit requests, validated and tuned against the actual corpus in PR 1/2. Count slice/string/index overhead, allocated capacity, coverage/retry arrays, and inventories under construction, not only serialized payload bytes. Reserve capacity incrementally before allocation; never build an unbounded map and check its size afterward. Domain/model context memory is separately charged under resource admission, so an inventory budget does not excuse an unbounded preparation cache.

When capacity is unavailable, queue only bounded request intent before construction, in normal priority order, and expose `request-memory` as the blocker. If one request cannot fit its limit, fail preparation clearly with scope/count/estimated-size information before claiming a finite population was admitted. Do not silently drop members, use a moving query, spill a scheduler database, or pretend partitioning into a different request preserves the same freeze. The required real full-library workloads must fit validated limits; an arbitrary low default that makes full rebuilds unsupported fails acceptance.

Release inventory references when requests finish and their bounded diagnostic retention ends; let ordinary Go ownership/GC reclaim them. Resource/task summaries may retain counts/digests, not the full membership forever. Measure the maximum bookkeeping for concurrent overlapping full-library requests, late historical inserts/edits, replaced revisions, and shared prerequisites. Dropping these inventories on restart is safe because existing domain pending/recovery state remains authoritative; finite intent/reissue semantics remain those in section 8.1.

## 7 Resource ownership and execution

### 7.1 CPU and model resources

All substantial discovery, membership construction, dependency invalidation, context preparation, computation, and publication work receives admission and attribution. A bounded readiness lookup may execute on the dispatcher; a full scan or long SQL query may not. Existing domains own the work inside the admitted phase, including its safe coarse boundary.

Define a CPU allowance as cooperative runnable parallelism, not assigned physical cores or an exact utilization percentage. One CPU slot permits one CPU-heavy worker; a native inference process configured for four intra-op threads reserves four slots. Count concurrently active preparation and inference together. A waiting coordinator consumes none.

A task declares a minimum useful allocation and a maximum useful allocation. Simple Go tasks can take a deterministic grant at each safe invocation. Model sessions instead use a stable execution configuration and a fixed active CPU envelope: if that envelope is temporarily unavailable, wait rather than start another thread configuration. When the configured cap shrinks or power changes, stop new admissions and retune/release excess work at an explicit safe boundary. Report any temporary over-budget drain.

Account for one GPU inference admission initially, consistent with the current model manager's serialized GPU resource. Account for model residency, preparation buffers, and temporary compute memory separately from active CPU slots. Warm model residency continues to consume memory, not active CPU capacity. Idle models can be evicted to satisfy a higher-priority request; preserve current backend and fallback behavior.

The model manager must receive its allowance from the same authority. Independent hidden preparation or inference budgets would invalidate the aggregate claim. Retain its model loading/reuse machinery and remove duplicated resource arbitration when conversion is complete.

Choose a finite default only after baseline measurements show the useful concurrency of current workloads. Benchmark at equal effective capacity first; separately test the deliberate throughput/responsiveness tradeoff of smaller user-selected caps. Do not silently halve parallelism and describe the slowdown as scheduler overhead.

A model runner's cache key currently includes threads and worker configuration. Therefore a different partial grant can cause another runner/resident model, even when the model itself is unchanged. Select the session's native thread/preparation configuration once from the task target and overall cap; reserve/release its fixed active envelope around batches while retaining only its idle memory/device residency. Do not opportunistically alternate between one-, two-, and four-thread runners as unrelated tasks release capacity.

For a deliberate target/cap/backend change, drain in-flight work, release the old active grant, retire the incompatible runner before starting its replacement, and preserve queued/committed progress. Low caps may use one-thread inference with serialized preparation; they must not deadlock while waiting for an envelope the cap can never satisfy. Test exact runner/model-load counts and peak residency during this transition.

Use preparation-context keys that reflect the metadata actually consumed. Ordinary message arrivals must not invalidate unchanged mirror/session context merely through an unrelated global generation. Conversely, do not keep stale lineage/classification state to avoid setup cost. Reuse or refresh supported components, and measure context builds/refreshes, scans, model loads, and buffer residency. Stable context reuse and efficient token packing take precedence over an arbitrary two-second batching target. Group compatible ready work within the same effective population/priority to avoid switching model/backend on every batch. This affinity cannot let historical work delay a newer recent request; record model switches and their cost. If the existing GPU manager permits only one resident runner, preserve that limit or explicitly separate resident-memory accounting from active-device admission before assuming two models can remain warm.

### 7.2 Scope of the allowance

Implement a shared per-process allowance first. Every managed structural stage in that process receives an explicit allocation, including when still invoked by the classic coordinator. Label preparation and native inference share it across models. Show the scope truthfully as “this library service” or “this command,” not “all Pharos on this Mac.” Separate service and CLI processes do not yet share a cap.

The integrated release must identify which substantial workers its CPU allowance covers and which remain outside it. The Thinking model-fitting pool in `thinking_models.go` is a real parallel pool, but its request/read-only uses are not automatically background tasks. Preserve interactive behavior; account for it separately or pass an appropriate foreground allowance when that path is deliberately integrated. Do not secretly make interactive queries wait behind historical background work.

Use the existing application broker as the proposed later host capacity authority only after the local contract is demonstrated. The broker would share grants across libraries and managed commands, without owning or duplicating their analysis graph. Normal `pharos serve` and CLI use continue without requiring a broker in the initial system.

Before offering a machine-wide setting, settle standalone participation, mixed-version processes, and restart behavior. Unmanaged/older writers may also bypass existing operation/file-lock paths; a new CPU allowance does not repair those safety contracts. Inventory these paths and either integrate them or clearly scope the guarantee. Do not add a second background runtime merely to ship the first useful allocator.

For eventual host grants, release only after acknowledgment or verified process termination, not a heartbeat timeout while a worker may still run. Reconcile broker restarts against actual process identity/start time. Until that is implemented and tested, the control remains explicitly per process.

### 7.3 Locks and bounded waiting

Acquire resource grants as a bundle. A queued task holds no catalog writer, source lock, or read snapshot while waiting for capacity. For an existing lock that cannot be acquired promptly, release the compute grant and requeue with the concrete blocker. Preserve a single documented lock order; avoid one task holding CPU while waiting for a writer held by work that needs CPU.

Checkpointed algorithm sessions may retain necessary context between batches, with its memory charged, but cannot retain a writer or unbounded read transaction while waiting for another admission. Algorithms that currently require a pinned read snapshot must finish that coarse task or release and rebuild it; do not imply that a cheap arbitrary yield already exists. Include any restart/rebuild cost and WAL retention in the comparison.

Disk-heavy operations retain existing I/O policy and safety locks. Initially limit them with a small concurrency admission rather than a new bandwidth controller. Memory accounting is an estimate used for admission, not a hard OS allocation guarantee. Record actual aggregate memory and disk use in experiments.

Do not globally change `GOMAXPROCS` to implement a background-only cap; interactive Go queries share the process. Bound the structural workers and native helpers explicitly. Inventory substantial CPU helpers that cannot honor a slot contract and label enforcement limits until they are integrated.

### 7.4 Resource monitoring and attribution

Resource monitoring is a deliverable of this work, not just temporary benchmark machinery. It has two complementary views: the total load generated by the observed Pharos processes, and each task's resource/work history. Neither view drives an automatic scheduler optimizer in the first version.

**Overall collection.** Extend the current process sampler rather than starting a separate sampler for every task or browser. Keep a bounded in-memory rolling history, initially proposed at 15 minutes, with CPU/memory/I/O samples around every two seconds while work is active and a slower idle interval around ten seconds. Retain the existing slower GPU cadence unless measurements justify a change. Sampling continues while the UI is closed, so reopening the resource panel shows the preceding work. A restart starts a new history segment; it does not fabricate the missing interval.

There is one owner for a sampled process tree. In the app, the existing broker can collect the managed process total and library subsets; a standalone service/command collects its own scope. Avoid multiplying native/driver queries when several libraries or pages are open. Reuse deduplication by process identity/start time and reconcile reaped child CPU without counting it twice. Show coverage when a process or short-lived child could not be measured. Observing the broker's aggregate tree does not require implementing the later Mac-wide resource allocator.

Inventory the native app and UI helpers as well as services, CLI commands, model workers, and other known children. Include native/UI processes when verified ownership is available; until then label the total as the covered managed Pharos processes and list the omission. Discover ownership through existing lifecycle/runtime records, not a process-name match that might include another app. Keep machine-wide load or memory pressure, if shown as context, separate from load attributed to Pharos.

**Task identity and ownership.** Attach the existing operation kind, operation/request identity, batch, model/backend, allocation, and active time range to task execution and its owned helpers. Reused model processes need an ownership interval for each admitted invocation; idle model residency is not automatically charged to the last task. Pass task identity into spawned Go workers and explicit native-worker requests, and close the scope when the admitted work and its publication/drain actually end. This instrumentation must work before and after executor conversion.

The task resource record uses the same `basis`, coverage, units, and measured/estimated/shared/unavailable distinctions in the API, UI, exports, and experiment reports. A resource grant is a separate field. Four allocated CPUs is not evidence that the task used four cores.

| Resource | Useful task-level measurement | Attribution rule and limit |
| --- | --- | --- |
| CPU | Cumulative CPU seconds and sampled usage relative to one core, plus wall-active time and wait time. | A dedicated helper with a known task interval can supply process counters. Multiple Go tasks and interactive requests share a process, so process CPU deltas alone are not per-task measurements. Add task labels to bounded diagnostic CPU profiles for sampled Go attribution; keep runtime/GC/native/unlabeled residuals visible. |
| Memory | Current and peak explicitly owned working buffers, native worker footprint when exclusively owned, shared model/cache residency, and allocation churn where available. | Whole-process footprint and peak remain measured aggregate facts. Go heap deltas and process RSS changes during an overlapping task are not that task's retained memory. Track owned buffers where practical; leave Go/SQLite/shared caches in a named shared bucket. Allocation bytes are not live memory. Heap profiles by allocation stack can find causes but must not be presented as universal task-label heap accounting. |
| GPU | Existing per-process/client accumulated GPU-time rates, task/backend inference elapsed time, and verified native device timings where supported. | Driver GPU-time share is best-effort and is not hardware saturation. Attribute to a task only with exclusive ownership and compatible sample boundaries or explicit device events. An interval spanning two tasks stays shared/ambiguous; inference wall time is not device execution time. Missing counters remain unavailable. |
| I/O | Task-counted logical bytes read/written, rows/messages processed, temporary bytes, plus OS process disk rates/totals where available. | Logical work and OS disk traffic have different units/bases: caches and SQLite/WAL/checkpoints can defer or share physical work. Do not assign the whole service's disk delta to whichever task happened to run. Row counts are work counts, not disk bytes; exited-child I/O may be incomplete. |

Task summaries should include committed units/input volume, queue/dependency/capacity/writer waits, elapsed time, allocations, and available resource totals/peaks. Show both live values and accumulated cost so a short expensive batch does not disappear in a long average. Store parent rollups as inclusive summaries and leaf measurements as exclusive contributions; never add both to the app total. Over the same interval, reconciled task CPU plus shared/unattributed residual should explain the observed process total within stated sampling/coverage limits; expose that residual rather than forcing an exact-looking split. Aggregate peak memory is the simultaneous process-tree reading, not the sum of per-task peaks recorded at different times. On Apple silicon, CPU/GPU memory can share physical storage; never add model/GPU estimates to process footprint as though they were disjoint allocations.

**No false precision.** A task table may initially show direct CPU/GPU figures for native helpers, tracked buffer/logical-I/O figures for an instrumented Go task, and a shared/unattributed entry for the rest. That is a coverage state to improve, not a reason to omit resource insight. Do not divide total load proportionally by allocated CPUs or elapsed time to manufacture per-task numbers. A task that is the only scheduled background operation can still overlap interactive Go queries, GC, and maintenance.

**Optional diagnostics.** Extend the existing bounded `/api/diagnostics/profile` path with task labels and workload metadata, rather than creating a new profiling service. Start with short explicit CPU profiles, optional execution/heap profiles, and the current native model diagnostics. Propagate labels deliberately into pooled/preexisting workers; do not assume context labels follow arbitrary queue messages. Profiles can explain task CPU hotspots, allocation churn, excessive scans, lock waits, and serialized preparation. Heavy sampling, device tracing, and forced synchronization remain opt-in with overhead reported. No continuous full heap dumps, forced GCs, goroutine thread pinning for attribution, or silent extra processes per task.

**Retention and cost.** Keep raw sparklines and batch attribution windows in bounded memory. Retain compact per-run/per-kind summaries through the existing compatible measurement/history payloads where feasible, with versions, units, workload features, and coverage. Diagnostic history is optional and disposable; it is never proof of task readiness or a new scheduler database. Existing journal retention/best-effort behavior still applies. Export selected history/summary/profile metadata for comparison without transcript bodies. If a new persistent representation would change catalog compatibility, separate that decision from the rollback-compatible executor release.

Measure monitoring enabled versus disabled on the same workloads. A proposed normal-monitoring target is no more than 2% extra aggregate CPU on representative busy work, plus bounded absolute idle cost, history size, and API payload size. Tune sampling or reduce instrumentation when this fails; heavy explicit profiling is reported separately and excluded from default-throughput claims. The 20% executor rejection boundary must not become a 20% monitoring budget. Use cheap counters on worker paths and aggregate off those paths, with no catalog writes or driver query per message/batch.

**Optimization use.** Compare each task kind/model/backend at similar input volume and cache/warmth using CPU seconds per committed unit, logical bytes per unit, working-memory/model residency, device/inference time, queue waits, and achieved units per second. This should reveal whether adding useful parallelism is possible, preparation or publication serializes the pipeline, model loads are repeated, buffers/caches dominate memory, or redundant scans dominate I/O. Keep shared costs and measurement coverage beside comparisons so faster scheduling is not mistaken for less computation.

## 8 Reversibility and storage compatibility

### 8.1 Disposable scheduling state and compatible data

“No algorithm changes” does not mean “no data changes.” Scheduled batches write derived rows, dirty tokens, model ledgers, and freshness state. Reversibility requires proving that those writes retain contracts the baseline understands.

The scheduler has no separate durable state file. Its ready queues, active selections, fairness counters, and reusable run sessions are in memory. On startup it reconstructs necessary work from the existing domain dirty inventories, generations, output provenance, and recovery jobs. Lost selection state can require rediscovery/replay; that cost belongs in the restart measurement.

During the comparison/rollback window:

- Keep the catalog schema compatibility floor, domain algorithm versions, record identities, output layouts, and source evidence formats unchanged.
- Keep existing dirty queues and domain checkpoints authoritative. A scheduler callback never acknowledges work on their behalf.
- Keep writes to old freshness/completion markers correct for old readers. A subset completion cannot set the old whole-stage “done” marker.
- Record operation progress and explicit request scope in existing operation/recovery records to the extent their current contracts permit. The operation journal is best-effort and pruned; it is an explanation/history surface, not a durable work queue.
- Preserve existing recovery and eject guarantees for captures, ingestion, backups, and derived-state publication. Do not change triggers or index definitions merely to store scheduling state.

A crash after a domain commit but before in-memory acknowledgment is harmless: reconstruct from committed domain state. An interruption before commit leaves the existing pending revision/recovery marker. Every adapter documents whether replay is idempotent or uses its existing recovery protocol. An intervening classic writer is handled by those same authoritative revisions; there is no stale scheduler database to reconcile.

Unfinished analysis remains discoverable after restart. Finite explicit recent-priority intent need not resume automatically in the first version: show the interrupted request when its journal entry exists, and let the user reissue the same scope. Never claim that a lost priority request completed or silently recreate it with a different time window. Existing durable source-repair/recovery jobs keep their current resume behavior. If durable arbitrary request resumption is later required, add a small explicit intent record under a separate compatibility decision, not a persisted execution graph.

### 8.2 One temporary execution seam

Introduce a process-start execution choice, `PHAROS_WORK_ENGINE=classic|scheduled`, during implementation and evaluation. The experiment harness sends this name as a request only; the stack's executor PR adds the option. The choice selects one owner for converted work for the lifetime of a process; the converted kinds are fixed by the release, not a growing set of per-task user flags. Changing it requires a controlled stop/restart; no hot switching halfway through a writer transaction.

Both choices use the same algorithms and supported data contracts. Unconverted work is explicitly inventoried during development. Every migrated launch site must route through the seam, including maintenance tails and read-started writes. No live dual-writing or background “shadow execution” on the same catalog.

Keep a built baseline artifact and its exact source commit outside the experiment workspace. The switch exists for controlled A/B comparisons during conversion; the retained old binary is the actual rollback mechanism. Test both while the switch exists because an in-build fallback cannot prove binary downgrade compatibility. Remove the switch and superseded coordinators no later than the release immediately following complete executor default, as specified in section 11.

### 8.3 Rollback procedure

1. Stop new scheduled admissions and ask active work to stop at its existing safe boundary. Verify all owned children/leases are stopped or accounted for.
2. Close the service normally and retain the operation/experiment report for diagnosis. In-memory scheduler state may be discarded. Do not delete source evidence, derived tables, user settings, or newly ingested records.
3. Start the retained baseline/classic executor against the current library. It discovers remaining work through existing dirty/recovery contracts.
4. Verify schema open, user records/preferences, recent ingestion, stop/eject behavior, and representative derived queries. Confirm pending generations are still discoverable.
5. If returning to the new executor, rebuild its in-memory selection from current domain generations and dirty records. Explicitly reissue any interrupted finite priority request that needs to continue.

A preflight backup is disaster recovery, not the routine rollback mechanism. Restoring an old catalog would discard the user's activity since the backup and is not acceptable as the definition of reversible scheduling.

Test rollback after a completed rebuild, mid-ingestion, after one derived batch commits, during model backlog, during relational publication, after a crash, and after the classic binary subsequently changes the library. If any data contract cannot satisfy this, stop that conversion and revise the boundary before production use.

### 8.4 Source removal

Keep algorithm extraction and scheduler routing in reviewable commits so the scheduler can be reverted without backing out unrelated analysis changes. During evaluation, old algorithms remain callable through the temporary classic coordinator. After acceptance, remove the duplicate coordinators and switch; preserve the baseline artifact and documented binary rollback for the stated compatibility window. Future algorithm/schema changes must make their own compatibility decision rather than relying forever on this refactor's guarantee.

## 9 Realistic throughput and correctness experiments

### 9.1 Establish a reproducible baseline before refactoring

Retain the baseline commit, built service and model workers, build settings, frontend bundle inputs, model/variant checksums, config/feature choices, timezone, and selected catalog/source snapshot identities. Model numerical mode and hardware backend must match across a paired comparison.

Historical measurements identify stages, large inputs, and likely regressions. They do not control input size, model warmth, machine contention, changed algorithms, overlapping maintenance, or when the whole library actually became current. Treat them as context and a check on realism, not the control arm.

### 9.2 Snapshot and replay preparation

Create a private immutable master corpus from an existing completed backup and its retained capture/preserved evidence. Start with the November 1 backup/replica and source snapshots described in [Incremental update cost](performance/incremental-updates.md), if they are still available and can be validated. This experiment must not open a read transaction on the live production catalog or create a new online backup of it. If no suitable backup exists, report that preparation requirement rather than reading the live database. Use APFS clones where supported; validate completeness including committed WAL contents, paths, model/schema versions, and free space. Never hardlink mutable database files between runs.

An existing writable replica may contain earlier edits, changed triggers, or evidence locators pointing at live files. Validate it against its backup manifest before reuse; prefer a fresh clone of the unchanged backup when provenance is uncertain. The incremental-performance notes identify evidence-path and inode caveats that the harness must account for.

Each arm starts from a fresh writable clone of the same master state. Reapply an identical workload descriptor before timing. Pin source evidence and local Git refs; reroute all configured source paths to the isolated corpus. Disable automatic production discovery, cloud uploads, backup destinations, external Ask/suggestion calls, and update/transfer side effects in the harness. Deterministically replay/stub external metadata where needed, and report that exclusion. Measure capture against isolated source copies through the real capture path.

Reuse safe isolation/path-validation primitives from `tools/dev.py`, but not its live-production snapshot path or whole query-development startup: it runs `build-tools` before serving, which would pre-complete part of a rebuild benchmark. The harness must leave the intended pending state intact. Model assets should be preinstalled and pinned; measure load/start cost explicitly rather than accidentally measuring a download.

Use actual retained workloads from the active user's library as the primary corpus. Include the largest realistic groups, old conversations with recent activity, mirrors, subagents, multilingual/large messages, reused text, and cross-conversation pastes. Small synthetic fixtures cover edge cases and tests, not the headline performance result. Keep private transcripts and raw benchmark artifacts outside tracked files.

### 9.3 Workload matrix

| Scenario | Initial condition and trigger | Completion criterion |
| --- | --- | --- |
| No-op and idle | Fully current clone; normal schedule/status activity | No unnecessary analysis writes or repeated full scans; measure a fixed idle observation window. |
| Ordinary incremental | Recorded real appends and metadata edits across the usual active sources | Captured/indexed inputs and affected enabled outputs reach their required quality. |
| Recent burst | A busy two-day slice, including additions inside older groups, against a substantial historical backlog | Time until the finite recent request is ready; separately time until the entire enabled backlog is complete. |
| New recent request during backfill | Inject the same request both at a safe batch boundary and while a real substantial coarse historical task is actively computing | Request-to-admission delay, actual product-query correctness and usable latency, and total completion including history/coarse-task consequences. |
| Full recapture and reindex | Same isolated source evidence and pre-rebuild catalog, real full recovery path | Source capture, indexing, verification, and required derived work complete; report any subsequent optional maintenance separately. |
| Derived rebuild | Same retained records with derived work invalidated through supported/task-specific test preparation | All required enabled derived domains current, including backlog tails and exact refinement where required. |
| Retained-message repair | Real retained repair request with its current product scope | The repair's declared output scope complete; do not silently compare it to a broader full rebuild. |
| Relational historical edit | Edit/backfill affecting copied text, templates, mirrors, or launches | Exact results match the reference full pass; required broader invalidation completes. |
| Selective backfill and feature change | Recompute one analysis over a selected scope; disable/re-enable an analysis with dependents | Selected outputs current; unaffected domains preserved; disabled dependencies explained. |
| Interrupt and resume | Stop/crash after controlled domain commits; restart same or other executor | Same final outputs and preserved pending work; measure redo and resume cost. |
| Interactive load | Replay a fixed representative set of reads while the rebuild proceeds | Same final freshness, plus request latency/error/cancellation measurements. |
| Multiple processes and power | Two realistic libraries/commands, CPU/GPU cases, budget reduction and battery transition | Initially verify each declared per-process allowance and report their combined use. Once host capacity exists, verify the shared cap; power changes take effect at safe boundaries. |

For real input replay, store arrivals/edits and their order in the descriptor. Keep the event timeline identical in each arm; a run-dependent wall-clock generator would produce different amounts of work. Separate correctness stress injection from the fixed-throughput comparison.

### 9.4 Match the work being compared

Record both user-visible request completion and **full settlement of the required work set**. Today's rebuild can return while model backlogs continue, and exact writing refinement differs from frequent estimates. Comparing the old request's early return with the new executor's full settlement would be invalid.

Define the required domains/quality and their expected pending counts in each descriptor. Optional disabled work is excluded identically from both arms. Independently inspect dirty inventories and generation/readiness state; do not stop merely because the API says a parent operation succeeded or a queue happens to be empty. Use an injectable experiment clock for schedule/day-dependent algorithms where possible and fix timezone/calendar boundaries. Eliminate unrelated timers or account for all work they introduce.

### 9.4.1 Product query correctness and usable latency

Measure the named surface through its real product query/API path, not only by asking whether a producer/projection dirty queue became empty. Message-label queries already combine stored facts with a live fallback for dirty conversations (`messageLabelStoredFrom`); a projection completing may make a correct query cheaper without making its data available for the first time.

PR 1 pins an actual UI-derived wire query and route, selected 48-hour scope and freeze provenance, accepted quality, expected result oracle, cache/warmth conditions, polling/replay cadence, and an absolute acceptable-latency criterion with sample-window size. Record these in the workload descriptor before measuring the candidate; do not move the threshold afterward. If the proposed label surface is already correct/usable through its fallback, report that result honestly and measure whether the new system reduces its latency/resource cost. Select an additional genuinely delayed scoped surface only as an explicit PR 1 decision, not after seeing candidate timings.

Record three separate milestones: `T_correct`, when the actual query returns the accepted correct recent result; `T_usable`, when it does so within the pinned latency criterion over the declared observation window; and `T_settled`, when the declared producer/projection/backlog work is durably settled. None substitutes for the others. A request completing under section 6.7 still reports its exact coverage, newer pending work, and the product query's relevant quality. Failed, partial, timed-out, or wrongly scoped responses do not count as readiness. Replaying the same read requests on both arms also accounts for live-fallback compute, cache warming, or structural work initiated by reads.

Include a recent request arriving while a real coarse historical task is already consuming CPU/memory, not only between batches. Inject at an observed internal progress/active-compute condition without adding an artificial yield; record remaining coarse duration, request-to-first-admission delay, `T_correct`, `T_usable`, and full settlement. Use a real relational/global pass alongside ordinary batched work. The two-second ordinary batch target does not promise two-second response behind an indivisible pass. If a named surface's latency goal cannot be met, either improve an existing safe boundary/admission policy within the performance budget or state that measured coarse limitation in the accepted contract. Do not hide it by delaying request injection, exceeding allocations, or claiming every global algorithm is preemptible.

### 9.5 Collect measurements

| Measurement | Reason |
| --- | --- |
| Monotonic wall time for request, first useful outputs, and full required settlement | Separates prioritized responsiveness from total throughput. |
| Aggregate user+system CPU seconds for service, broker participation, model workers, and all owned helpers | Detects extra work hidden by concurrency. Reconcile exited children without double counting. |
| Peak simultaneous resident/footprint memory and sampled memory over time | Detects increased parallel buffers/model residency. Sum concurrent samples, not unrelated per-process maxima. |
| Logical read/write bytes, available process disk counters, WAL peak/growth, and temporary storage | Detects rescans, checkpoint pressure, and scheduling write amplification. Mark unavailable/partial counters. |
| Input/output counts, bytes/messages/tokens, committed units, scans, model loads, replayed batches | Establishes equal work and identifies duplicated preparation. |
| Stage/batch elapsed time, queue/dependency/capacity/writer waits, boundary duration | Explains latency. Do not attribute process-wide CPU to a task while unrelated work overlaps. |
| Interactive read p50/p95, maximum observed latency, errors and cancellations | Protects service usefulness during catch-up. |
| Output digests and bounded semantic differences | Establishes correctness, not just fast operation completion. |

Use the collector and task metric definitions in section 7.4 for both ongoing monitoring and benchmark exports; do not build a second resource accounting system. Measure the complete owned process tree; do not rely on status polling being open. Use the same collector with its cadence recorded in the workload descriptor. An explicit benchmark may request one-second CPU/memory/I/O sampling instead of the proposed two-second normal active cadence; apply it equally to both arms, preserve the separately supported GPU cadence, and measure observer cost. Collect monotonic timings and final process counters for short-lived helpers. For GPU work, record backend, inference time, process resources, and available device counters; do not pretend CPU seconds measure GPU compute. Record observer overhead with instrumentation off/on before using it for acceptance.

### 9.6 Measurement effort and paired run protocol

Use three tiers rather than repeating the entire matrix on the full library for every PR:

| Tier | When | Required runs |
| --- | --- | --- |
| Representative slice | Routine implementation and tuning | One or two paired runs of the affected real workload, plus the relevant correctness/failure scenarios. Repeat when counters or variance warrant it. |
| Internal producer-to-consumer proof | During domain conversion, before declaring the abstraction suitable for the complete stack | Paired real-workload runs through that path, its historical tail, durable dependent discovery, shared-demand cancellation, and restart/old-binary proof. Use full context when required; this is not a separate production release. |
| Integrated stack candidate | Before its single production cutover or a later material executor change | Full-corpus baseline/candidate pairs for full recapture/reindex and derived settlement, plus targeted checks for changed behavior. Confirm concerning/noisy results in reverse order; do not run every failure scenario as a full rebuild. |

The PR 1 preparation first runs classic versus classic on the representative slice to establish harness parity and observer/cache variance. Reuse its immutable source/corpus descriptors in later phases. A new comparison needs new runs when the executor, resource policy, algorithms, model/backend, or relevant corpus version changed; historical stage times do not replace them.

PR 1 also needs one baseline full-rebuild observation if existing records cannot reconstruct recent-readiness milestones. Use it to identify waits and validate the workload, not to run the whole scenario matrix at full size. Re-run the baseline alongside the candidate before release when conditions have changed; a weeks-old wall time is not a paired control.

The slice must retain the dependencies needed for the measured output: mirrors, parent/child/reuse links, large groups, and relevant historical text. For a global relational task, use its full prerequisite context or the full corpus; an isolated recent slice cannot establish exact full-library parity. Record its size/composition and never extrapolate slice timing as the final rebuild result.

Build once before timing. Snapshot/copy cost is separate; startup, discovery, preparation, publication, and required checkpoints are included in execution. Use the same Mac/storage/power mode, pinned models/settings, and equivalent co-running activity. Never run A/B arms simultaneously on the same Mac, stop the user's production service automatically, or clear machine caches to manufacture a number.

Start each paired arm from an equivalent fresh writable clone. Alternate ordering across available pairs; perform an additional reverse-order pair when a wall-time result looks near the rejection boundary, counters disagree, or cache/thermal conditions differ. Record every run, paired ratios, and ranges. A timeout/error is a failed experiment, not a fast sample. If the available evidence cannot separate a material regression from cache noise, do not pass the gate; collect the next targeted pair or repair the bottleneck.

Prioritize aggregate CPU seconds, comparable logical write bytes, committed work counts, scan counts, and model loads for diagnosing avoidable overhead. They are less sensitive to page-cache warmth than wall time, but must still be measured over the same work. Report peak/sustained memory and full settlement wall time as well. Recent-readiness improvement is a wall-time claim and requires its own paired evidence. The user's wall-time degradation boundary remains a gate, even when CPU work did not increase.

Separate fresh-process/model-load from warm-process cases. A clone or fresh process is not an OS/SSD-cold machine. The existing incremental measurements observed roughly 20-second wall-time changes from cache conditions; do not enforce a statistically unsupported 5% wall-time promise on that machine. Keep the same-calendar-day/clock behavior consistent so day-rollover rebuilds do not warm only one arm.

Compare baseline to the new executor at equivalent useful capacity, and separately compare proposed defaults or deliberate smaller user caps. This distinguishes overhead from an intentional resource tradeoff. A faster recent subset cannot conceal a slower full required settlement.

### 9.7 Acceptance gates

The following are proposed engineering gates for critique; the user's firm requirement is to reject a significant degradation at the 20% level.

- Exact semantic correctness and rollback checks pass. A changed result must be explained by an explicitly fixed clock/version or a separately reviewed algorithm change, never waved away as reordering noise.
- Aim to keep total work/resource costs close to baseline. An approximately 5–10% increase in comparable aggregate CPU seconds, logical writes, scans, or model loads triggers profiling and a documented explanation; do not turn noisy wall-time differences into a precise 5% pass/fail threshold.
- A reproducible ratio of **1.20 or more** for full-rebuild wall time, aggregate CPU seconds, peak aggregate memory, or comparable logical disk writes rejects the candidate. Check affected primary scenarios separately; do not average away an expensive workload. Read-byte amplification is also investigated and rejected when it reflects a reproducible 20% increase in required resource work rather than cache-counter noise.
- Lower CPU/GPU caps are an explicitly requested policy tradeoff, measured and reported separately. They cannot excuse an overhead regression at the same allowance or a poor default.
- Idle overhead, WAL/temp growth, and memory over time remain bounded; a low peak does not excuse a leak or a long tail. Set absolute idle and storage bounds after baseline collection.
- Interactive request errors do not increase. Propose a 10% p95 latency regression investigation threshold alongside absolute latency and sample counts, so near-zero/noisy cases do not dominate a ratio.
- The recent-first workload demonstrates less time to its required readiness than classic execution when independent scoped work exists. Global prerequisites may limit that benefit; those cases must explain the dependency and show no avoidable duplicate work.

Keep a compact report in the repository with corpus counts/checksums, commits/configuration, all paired ratios, semantic comparison results, and a decision. Large/private samples, traces, and profiles stay in a private experiment directory. No performance numbers are asserted by this design before the harness runs.

### 9.8 Correctness comparison

Compare canonical source records, domain outputs, model identity/input hashes, label/event membership, authorship spans and dispositions, mirror suppression, usage/tool totals, finding results, search membership, and relevant freshness/dirty state. Enumerate the comparison per migrated task before accepting it.

Normalize only execution metadata with no semantic effect: operation IDs, elapsed times, scheduler cursors, and documented timestamps. Do not normalize away missing records, different lineage, extra duplicates, pending revisions, incomplete quality, or a changed global total. Use stable sorted keyed comparisons and digests for the full corpus, with bounded keyed differences for diagnosis. Floating-point tolerances, if required by the same GPU backend, must come from the existing numerical contract and cannot hide changed classifications.

A clone that has run the candidate is then opened by the retained old binary, updated with another real replay segment, and rechecked. The same exercise runs old → new → old and across interruption boundaries. Raw/canonical records and user settings must survive each round trip.

## 10 State explanations and user controls

Extend the existing activity/background surfaces and expose one structured inspect response usable by CLI, UI, and agents. Proposed fields include request identity/scope, requested and effective prerequisite populations, required quality, observed input versions, current/queued batches, committed progress, concrete blocker IDs/reasons, allocated resources, and the next safe boundary.

For example, an explanation may say:

```json
{
  "task": "analysis.labels",
  "population": "recent-request-17",
  "state": "blocked",
  "reason": "dependency",
  "depends_on": {
    "task": "analysis.authorship",
    "scope": "selected-conversations-and-reused-inputs",
    "quality": "usable-stored-estimates"
  },
  "committed_units": 42,
  "remaining_units": 18,
  "cpu_slots": 0
}
```

This illustrates the proposed contract and an estimate-compatible writing dependency, not an existing endpoint. Exact-refinement requests would name that stronger quality and its real expanded scope. A real declaration must match the consumer's current accepted inputs.

Store structured facts; render prose from them. An agent should be able to answer “why hasn't this completed?” from the inspect response without reading implementation logs. Use the existing operation state vocabulary and distinguish blocked dependency, capacity, power, policy, writer, external failure, and coarse running unit. Keep a bounded recent decision trace and aggregate counters; do not retain a permanent log row per message or timer tick.

Controls should remain small: existing per-analysis enablement and cadence, a shared background CPU allowance, existing model memory/backend choices, explicit run/backfill scope, pause/stop/resume, and an optional recent-first request. Do not expose internal batch tuning as normal product settings. New durable UI inputs follow the view-state contract; settings and actions receive the appropriate explicit classification.

### 10.1 Resource history above the current metrics

Add a compact aligned sparkline strip above the existing CPU/GPU/RAM/I/O values in the resource panel. Keep those current values visible. Use separate labeled series with the same time axis and their own units: CPU as percent of one core (which can exceed 100%), GPU as measured driver-time share, RAM as physical footprint, and I/O as bytes per second. Do not combine percentages, GB, and bytes/s into one unexplained load score. Storage/WAL sizes remain separate quantities.

The chart follows the panel's scope: app view shows the covered Pharos process tree, library view its subset. Identify the scope and covered processes. A lightweight hover/pinned inspection shows timestamp, values, coverage, and the active task kinds at that time; temporal overlap is not a claim of exclusive attribution. Use aligned batch/start/stop markers to connect spikes to execution without producing a row or graph for every message.

Preserve gaps, unsupported metrics, stale baselines, partial lower bounds, and restarts. Do not interpolate missing GPU measurements as zeros or repeat an old value as a fresh sample. Use actual metric timestamps even when GPU and CPU sample at different cadences. Keep accessibility, a textual current/window summary, narrow layouts, and reduced-motion behavior; avoid chart animation and a new visualization dependency for four small series.

### 10.2 Task resource details

Task rows/details show elapsed and wait time, allocated CPU allowance, measured/sampled CPU, available GPU time, owned/shared memory, logical/OS I/O, and useful work counts. Unavailable/shared values have an explicit basis instead of a guessed number. Show current, cumulative, and sampled peak quantities where meaningful, and retain a bounded completed-task summary so a finished expensive task remains inspectable.

A task-kind summary supports comparisons across runs/model versions at similar workloads. Include an export action for structured measurements and a bounded diagnostic profile action in the appropriate details surface. UI and machines consume the same accounting record; prose and chart tooltips must not drop its coverage/basis. Start with useful aggregate history and direct task measurements, then expand attribution coverage as tasks convert.

## 11 Four stacked PRs and one integrated cutover

Build a robust replacement in a small handful of stacked PRs. The stack is a review/implementation structure, not a commitment to ship each lower layer or partly converted pipeline. Each PR builds/tests against its parent; the original production executor remains selected until the complete stack passes the integrated gate. Lower PRs can merge without enabling unfinished execution, or the whole stack can remain unmerged until review is complete.

The shipped scope includes unified execution/ownership for classified structural work, population-aware recent-first behavior wherever its real correctness scope allows it, explicit CPU allocation, bounded failures and request semantics, aggregate resource history, task metrics/explanations, and diagnostic access. Global relational tasks remain explicit global tasks, not unfinished conversions. Specialized safety/lifecycle operations retain their own recovery protocols with reviewed execution/resource boundaries.

| PR | Review boundary | Production behavior before the full stack |
| --- | --- | --- |
| 1 | Contract, inventory, baseline harness, and domain correctness/rollback proofs | Existing execution. Test-only probes and inert metadata/instrumentation hooks are allowed; no new scheduler owner. |
| 2 | Common runner/resource contracts developed with timing and a minimal real producer-to-consumer path, plus foundational telemetry | New components are testable but dormant. Do not ship a standalone CPU-control or telemetry rollout as a prerequisite. |
| 3 | Remaining domain conversion and complete population pipelines | Internal proof and full-path testing on isolated corpora. No separately supported partially converted production mode. |
| 4 | Resource UI/inspection, integrated acceptance, cutover, and deletion of superseded launchers | One cohesive candidate release after all gates; retain only the narrow comparison path for the stated acceptance window. |

Mac-wide resource arbitration remains a separately justified extension of the same allocation contract, not an architectural dependency of these four PRs. The initial allowance is explicitly per process, while the existing broker can already observe aggregate owned processes. Do not add a fifth fundamental scheduling layer to make the first integrated release possible.

### PR 1 Contract and proof harness

**Build:** finalize the task × scope × accepted-quality table, structural launch-site inventory, request/priority/failure semantics, and domain invalidation ownership. Retain the known-good binary/source state and validated backup/replica. Build the reproducible workload runner, process/work measurements, settlement assertions, and semantic comparisons by reusing existing tools.

Measure ordinary catch-up, model/version backfill, and a full rebuild where needed. Pin a named user-visible surface through its actual product query, proposed as the existing recent message-label query for the selected 48-hour population, and measure both correctness and acceptable latency under section 9.4.1. Account for its existing live fallback; do not substitute projection settlement for usability. Break waits into discovery, global context, unrelated history, model capacity, ingestion exclusion, and publication; record exact prerequisite scope and quality. If that surface has a genuinely global barrier, name it and choose a second scoped surface rather than inventing false locality.

Make durable dependent discovery the first data-safety proof. Establish the reference with today's full-domain discovery plus a bounded initial execution; the minimal real selected adapter must pass the same reusable oracle in PR 2, then remaining adapters repeat it in PR 3. For labels, construct a selected producer plus out-of-population historical fork/reuse consumers; process the producer, interrupt at publication boundaries, and reopen with the retained old binary. All affected dependents must remain discoverable. Include missing-output/structure/model invalidation and newer input arriving during the commit. Keep this in domain code; do not implement invalidation through a scheduler-owned graph.

Audit resource measurement coverage and A/A observer/cache variance. Establish compact replay scenarios for overlapping requests, cancellation, manual stop/rediscovery, continuous edits, a failing first unit, and a cap smaller than a model's usual configuration. Implement their executable proofs with the runner and first real adapters in PR 2, then repeat them across the remaining adapters in PR 3. Pin the compact inventory representation/budgets and product-query latency criterion before treating the execution contract as settled.

**Deliverables:** a measured recent-readiness breakdown through the pinned product query, distinguishing correctness, usable latency, and projection settlement; a producer-to-consumer correctness/old-binary rollback proof at actual commit boundaries; and a bounded internal pipeline proof specification with scope, quality, memory inventory limits, and success criteria. Also retain the comparison corpus/configuration and an explicit deletion inventory.

**Gate:** classic A/A parity; no live catalog transactions/external side effects; correct full settlement; preserved canonical/user data; a validated explanation of locality/global barriers. Any boundary lacking proof stays coarse in the conversion. This gate establishes an engineering contract, not a market experiment that decides whether maintainable execution is worth doing.

### PR 2 Common execution and resource contracts

**Build:** develop the small task table, in-memory population/demand selector, deterministic batch loop, explicit allocations, bounded retries/blockers, and shared execution ownership alongside real adapters. Include stored timing and a minimal real writing-input → labels → pinned product-query consumer path, with admitted durable discovery/context preparation and actual publication. Reuse the existing relevant domain functions; mocks alone do not establish the adapter contract. Expand this path only far enough to prove the demanding boundaries, then use PR 3 for the rest.

Implement sections 6.6–6.9 so request cancellation cannot kill or retire another request's work, manual task stops cannot bounce through rediscovery, changing inputs cannot force unbounded revision chasing, and finite inventory memory is actually bounded. Keep `Next` cheap; the first real adapter must prove that a broad metadata scan runs under admission rather than blocking dispatch.

Use independently live executor-owned operation roots for actual shared execution and references from request operations; prove registry/UI/journal retention and lifecycle safety after the original requester is retired. Keep domain pending state authoritative and all scheduler state disposable. Preserve existing locks and data formats; validate cycles and unknown launch classifications without a new workflow database.

Implement stable model/preparation sessions and allocation admission together. Pass allocations into stage code; do not resize native runners opportunistically per batch or create competing hidden preparation budgets. Low-cap, target-change, power-change, and worker-failure tests must preserve progress and bound residency/model loads.

Implement the bounded aggregate history collector, task/batch identity and helper ownership, cheap work/wait/resource counters, summary/export records, and labels for the existing bounded diagnostic profile path. Reuse the same measurements in the harness and eventual UI. Missing/shared attribution remains explicit; no new per-task process isolation or continuous heavy profiling is needed to make the numbers look complete.

**Gate:** both real adapters and the minimal producer-to-consumer path run through the same contract, preserve out-of-population invalidation through actual old-binary recovery, and produce the pinned correct product result with measured usable latency. Include admitted discovery/preparation, shared execution after requester pruning, inventory peak-memory/admission limits, continuous edits, low CPU caps, and failure/stop cases. Confirm stable sessions/context reuse and no resource double counting. If the hard adapter needs another executor branch or an unbudgeted scan, revise the common contract here before expanding it. Monitor overhead/history bounds; all new execution stays dormant in normal production until the integrated cutover.

### PR 3 Complete domain adapters and population execution

**Build:** expand the real adapters and producer-to-consumer contract already proven in PR 2 to the remaining reviewed domain scope, including the rest of the writing/labels/events/projection pipeline. Keep conversion in independently reviewable commits within this PR. The first hard adapter has already exercised discovery/publication/session semantics; do not postpone that proof until the full conversion or ship it as a separate narrow product.

Domain adapters retain durable dependent discovery before selection, actual quality/readiness checks, efficient shared context, ordered model framing, dirty-token publication, and existing recovery. Test old conversations with new activity and dependents outside the selected population. Replace historical backlog launchers with ordinary low-priority demand; no admitted batch may leave a hidden compute tail after releasing its grant.

Convert acquisition/indexing plans and retained repairs to their safe natural units. Narrow ingestion/projection exclusion only at proven committed publication boundaries. Extend recent-first through the scoped consumers the contract actually supports; represent exact authorship, global grouping, and global comparisons as complete coarse tasks when required. Keep broad barriers only for actual data semantics or safety, with explicit limitations and tests.

Route background, run-now, backfill, read-started structural work, and maintenance through one classified owner. Integrate specialized safety work at its established boundaries without rewriting cloud/capture/eject protocols. Remove bypasses and superseded launchers as ownership moves; the thin classic comparison path may invoke the same domain algorithms but is not a second newly evolved executor.

**Gate:** the internal producer-to-consumer proof demonstrates recent usability before historical settlement when independence exists; the old binary can recover out-of-population dependents after every tested interruption; shared request/failure/continuous-edit cases hold with real adapters. The full launch-site inventory has no unreviewed owner or permanent conversion exemption. Run paired slice tests across affected algorithms and start the full integrated corpus comparisons; do not release a partly converted subset to satisfy an artificial milestone.

If the diff is too large for clear review, split code movement from behavioral changes into commits within the stack. Add a fifth PR only for a concrete independent review boundary documented during implementation, not a new user-visible rollout phase or a flag for each analysis.

### PR 4 Integrated observability and production cutover

**Build:** finish the resource-history strip, task resource details, shared-work references, blocker explanations, exports, and bounded profile actions in the existing surfaces. Aggregate history, task identity, work counts/waits, directly available CPU/GPU/memory/I/O measurements, and honest shared/unavailable fields are part of this release. More detailed attribution can improve later without pretending it already exists; expensive GPU tracing or perfect Go heap ownership is not an acceptance gate.

Wire the complete executor behind the single process-start comparison choice. Keep lower-stack code dormant under classic selection until this PR's candidate is explicitly enabled. Remove duplicate regular launch paths, resource arbiters, and progress logic; retain only what the bounded A/B/classic fallback actually needs. Update the operation/background contract, technical overview, authoring guide, monitoring definitions, and rollback procedure with the final implementation.

**Integrated gate:** full-corpus paired baseline/candidate runs for full recapture/reindex and derived settlement at equivalent useful capacity; actual product-query correctness/usable-latency evidence for the pinned surface, including arrival during active coarse history; semantic parity; selected backfill/disable/re-enable; sharing/cancellation/manual-stop behavior; bounded failure/supersession; stable session/model-load counts; stop/eject/crash and actual old-binary downgrade at publication boundaries. Normal telemetry must pass its own overhead budget. Meet the 20% rejection boundary and investigate smaller material regressions. Pass the simplification acceptance test in section 12 as well as correctness/performance gates. Use targeted slice/fault tests rather than multiplying full rebuilds for every failure case.

**Production:** publish one cohesive candidate for the active user after the full stack passes. An initial controlled use of the complete system is acceptable; separate partial CPU-only, pipeline-only, and monitoring-only releases are not prerequisites. Observe representative active incremental use, one completed production-scale rebuild, and a selective backfill plus stop/resume, checking recent coverage, background fairness, latency, resource history, and pending domains. Classic remains a clearly selected fallback during this acceptance window; there is no silent fallback that hides failures.

After acceptance, keep the executor as the sole normal path. Retire the comparison switch and retained classic coordinator in one mechanical cleanup PR no later than the next normal release. This small cleanup is not another architectural rollout. If acceptance fails, return to classic or the retained old binary and fix/retest the complete candidate; do not accumulate an indefinite hybrid. Detailed attribution improvements and optional host-wide arbitration remain follow-ups to this same contract.

### Stack rollback and release boundaries

Stacked PRs make dependencies reviewable; they do not make arbitrary middle-layer reverts safe. PR 3 depends on PR 2 and PR 4 depends on the complete lower stack. Revert a dependent suffix in reverse dependency order, or revert the complete stack, rather than removing the resource/runner contract from beneath converted adapters.

Before activation, a merged lower prefix is inert in normal execution and can be reverted without undoing unrelated algorithm changes. After activation, first stop/drain work and use the explicit classic selection or retained compatible binary under section 8.3. Preserve all current canonical/user/derived records and pending work. Then revert PR 4/cutover or the necessary complete suffix while offline, verifying the resulting prefix still builds and has one execution owner. Reverting the UI/cutover alone returns to classic dispatch; it does not prove the new allocator/monitor is free of overhead, which is why actual previous-binary rollback remains required.

Record exact commits/build artifacts and the accepted catalog/model compatibility window for the stack and retained baseline. Include the old-binary recovery and performance evidence with PR 4 so a rollback can be executed from the document without reconstructing the development discussion. Source reverts are not a substitute for proven data compatibility, and restoring an old catalog is not routine rollback.

## 12 Testing and failure cases

Use ordinary Go tests for dependency/readiness logic, priority, batching, invalidation, and state transitions. A fake clock and controllable completion events should make queue tests deterministic; no fixed sleeps and no tests of exact implementation SQL. Use the existing `testCatalog`, `testServer`, fixture/override patterns, and process isolation. Extend the owning test files when modifying existing behavior.

Required behavior coverage includes:

- A selected unit's downstream stage becomes ready after its committed prerequisites, while unrelated history remains pending.
- A recent message in an old conversation is selected; an unknown timestamp is accounted for; closed compact membership survives late matching historical inserts, activity edits, and deletions without silently growing or dropping members. Construction, overlapping full-library requests, coverage arrays, and inventory retirement stay within centrally enforced budgets.
- A relational prerequisite expands scope; estimates do not satisfy an exact dependency; a partial output cannot advance global freshness.
- A historical insert/edit/delete, mirror change, graph change, model/version change, and local-day/timezone change invalidate the correct existing scopes. Coarse fallback remains correct.
- A newer dirty token survives old-batch publication. Discarding all in-memory scheduler state cannot suppress necessary work.
- Repeated run-now/backfill requests coalesce without suppressing genuinely different input revisions or quality requirements. Cancelling the first requester leaves other explicit/automatic demand alive; cancelling the last demand safely stops unneeded compute.
- A task Stop holds its dirty scope against immediate rediscovery, a request Cancel detaches only that request, and Resume/restart/durable pause follow the stated policy. After the initiating request is UI-filtered and registry-pruned, shared work remains visible, stoppable, resource-accounted, journaled, and part of lifecycle/eject checks under its independently live root.
- A user-started full rebuild's historical remainder yields to a newer explicit recent request; request FIFO applies only after classifying remaining work.
- Continuously edited selected units exhaust the bounded replacement allowance with an honest interrupted outcome and leave newer dirty work pending. No success claim combines incompatible producer/consumer revisions.
- A permanently failing first unit backs off or blocks without starving runnable units. Retry ownership stays with one layer, and bounded error state cannot forget failures into a hot loop.
- A recent producer's historical fork/reuse dependents remain durably dirty after source publication, scheduler-memory loss, and an actual old-binary restart.
- Feature disable blocks/cancels affected work at a safe boundary, preserves retained outputs, and explains dependent readiness. Re-enable discovers pending work once.
- CPU preparation plus native workers respect a single budget; parents do not double count; GPU residency/admission and memory release remain bounded. Changing free capacity between batches does not multiply runners/context builds; deliberate cap changes retire incompatible sessions safely and low caps make progress.
- A waiting task holds no writer/compute grant that prevents its own prerequisite from progressing. Expensive discovery, membership construction, invalidation, and preparation acquire admission and attribution before starting; a bounded `Next` cannot hide them. Oversized resource/inventory requests are queued or rejected under the explicit contract without unbounded allocation or a moving-query fallback.
- User stop, power change, eject, process exit, and worker crash preserve data and release capacity only when ownership truly ends; broker restart receives the same checks when host-wide capacity is implemented.
- Classic/scheduled and old/new binary round trips preserve both canonical and derived state, with safe replay where needed.
- Old/background/CLI writers after a candidate run change the authoritative pending state used to reconstruct the next selection; there is no persistent scheduler completion state to trust.
- One bounded collector continues without an open UI, deduplicates multiple readers/process identities, and leaves real gaps across restarts, PID reuse, missing GPU clients, and unsupported counters.
- Allocation, measured CPU, sampled CPU, owned memory, shared residency, allocation churn, logical I/O, and OS disk traffic retain distinct units and attribution. Shared Go work cannot inherit the whole process delta.
- Reused helpers change task ownership only at verified execution boundaries; exited-child CPU is reconciled once and unsupported final GPU/I/O remains partial. Parent-inclusive rollups are not added to leaf usage.
- Sparkline timestamps, irregular GPU samples, scope changes, gaps/partial readings, and completed-task resource details match the underlying records. Browser tests check accessibility/layout/interaction; numeric transforms and rolling-buffer logic use unit tests.
- Monitoring/history/exports stay within configured bounds; profiling sessions end; telemetry remains disposable and never changes freshness or rollback compatibility.

### 12.1 Simplification acceptance

Treat maintainability as an acceptance gate, not just a hoped-for consequence. Demonstrate an ordinary independent serial analysis registered through its domain adapter and declaration, using real pending/publication semantics in a small test domain. It must receive selection/admission, cancellation, retries, progress, inspection, and backfill behavior without a new executor kind-switch branch, background goroutine, task-specific retry loop, or bespoke progress renderer. Remove the demonstration from production registration or keep it test-only; it is a proof of the extension path, not a new feature.

Review the deletion inventory and final launch-site ownership alongside that demonstration. Every retained special orchestration path needs a concrete algorithm/safety reason; a simple adapter must not be hiding its own scheduler. Additional tests and monitoring may increase total lines, so net line count is not the criterion. The criterion is one generic execution path for an ordinary task and fewer independent ownership/policy implementations.

Keep realistic performance experiments opt-in, separate from unit-test timing assertions. Add fault injection around actual domain commit and in-memory acknowledgment boundaries rather than terminating an arbitrary process after an arbitrary sleep. Public-facing UI changes receive filtered browser coverage for focus, layout, pending state, and restoration; calculation assertions belong in Go or JS unit tests.

Run the fast Go tier on implementation changes. Before each PR run `go test ./...`, `npm --prefix web run check`, and `npm --prefix web run test:full`; build the frontend before Go compilation when frontend sources change. Run applicable manual macOS lifecycle/eject tests only in the implementation phases that change those paths. Keep generated bundles untracked. Run the opt-in comparison matrix as a release gate for executor changes, not automatically in every routine test invocation.

## 13 Long term maintenance workflows

**Add an analysis:** reuse the operation kind and default serial allocation, implement selection/batch processing against its domain pending state, and declare its actual prerequisites. Keep quality, invalidation, publication, and semantic comparison with the owning algorithm. Defaults should avoid extra objects for independent serial tasks. Reuse progress/cancellation/backfill handling; a task with genuinely global semantics declares that plainly.

**Remove or disable an analysis:** stop admitting its work, drain/cancel at safe boundaries, preserve or retire existing output under its domain policy, and explicitly resolve the effect on dependent tasks. Disabling a producer does not make its consumers current.

**Run a backfill:** submit an ordinary request with a finite population, task/model version, required quality, and priority. Default to low priority; a deliberate run-now promotes the request and its required prerequisites. Before accepting a promise of restart recovery, express unfinished work through the domain's existing supported dirty/recovery mechanism. A forced rerun of already-current data that has no such representation is explicitly interrupted on process exit and requires reissue; an in-memory cursor cannot guarantee durable backfill intent. Do not add a task-specific background goroutine or silently invalidate the whole library to emulate an unsupported selective backfill.

**Invalidate:** use the analysis's real dirty units and existing generation/algorithm versions, propagate required downstream invalidation through declared dependencies, and coalesce duplicate demand. Preserve existing triggers as the source of truth. Start with conservative broad invalidation where exact locality is unknown; improve locality with a measured domain change rather than inventing arbitrary graph edges.

**Change an algorithm:** perform its normal versioning, correctness review, and migration/backfill decision separately from scheduling. Replay the benchmark workload and semantic oracle appropriate to that change. Scheduler code should not need new special cases.

**Debug stalled work:** inspect the finite request, dependency unit/generation/quality, blocker, allocation, committed cursor, and safe boundary. If those fields cannot explain the state, fix the contract instead of adding another bespoke logging path.

## 14 Decisions to settle during review

The following questions are deliberately bounded; they should be answered before implementing the affected PR.

| Decision | Proposed starting point | What could change it |
| --- | --- | --- |
| What does “up to date” require for each product surface? | First PR 1 deliverable: complete the task × scope × quality table in section 4.3 from actual consumers. | A consumer currently relies on undocumented mixed/provisional semantics; resolve before its conversion. |
| What is the first safe population boundary for ingestion? | Existing provider group/workspace commits; whole conversation framing for models. | Actual canonicalization or source transactions require a wider scope. |
| Does scheduling need persistence? | No separate scheduler file; use the compact admitted inventories in section 6.9, reconstruct work from domain state, and keep the operation journal diagnostic. | A demonstrated durable-intent requirement needs separate review; a moving predicate is not a substitute for frozen membership. |
| How is user-visible readiness measured? | Pin the actual product request, quality, oracle, and absolute latency criterion in PR 1; measure `T_correct`, `T_usable`, and `T_settled` separately. | Existing live fallback may make an apparently blocked projection already usable. |
| When does the allowance become Mac-wide? | Per-process first; later reuse the allocation contract through a broker authority if simultaneous-process measurements justify it. | Standalone/restart/version coverage may make the host implementation too costly for the benefit. |
| Which tasks can pipeline immediately? | Existing dirty-unit maintenance/model paths with proven committed prerequisites. | Readiness/identity/relational coupling requires a wider barrier. |
| How long is a batch? | Existing sizes, soft 0.5–2 second ordinary target, explicit coarse exceptions. | A/A and paired measurements show excessive overhead or poor responsiveness. |
| What is the default CPU allocation? | Choose from measured current useful concurrency, then test responsiveness. | Hardware/backend differences make one fixed count unsuitable. |
| When is old coordination deleted? | Remove ordinary duplicate launchers within the stack; retire the thin comparison path by the release immediately after integrated acceptance. | A failed gate postpones acceptance or causes explicit rollback, rather than an indefinitely extended hybrid. |

Avoid adding a general-purpose DAG database, a resource-utilization optimizer, new analysis semantics, or an abstraction for every possible future partition to answer these questions.

## 15 Requested critique

Reviewers should challenge the plan on concrete failure modes:

1. Can a batch commit or acknowledge freshness in a way the old binary misinterprets? Is rollback safe without restoring an old catalog?
2. Are any proposed population boundaries unsound because of lineage, chronology, mirrors, shared source groups, or global comparisons?
3. Can all scheduling state be discarded safely, including after an old writer modifies the library? Is any proposed durable request intent actually necessary?
4. Is the per-process allocation simple and honest about coverage? Does a later host authority earn its complexity without blocking useful earlier releases?
5. Are any launch sites, background tails, read-started writes, model helpers, or safety operations outside the inventory?
6. Does the experiment compare the same work and quality, including backlogs, refinement, publication, and verification? Can cache order or live source access bias it?
7. Will batching repeat global preparation, enlarge WAL retention, or fragment inference enough to fail the performance requirement?
8. Are priority, history fairness, dependency failure, and disabling understandable from one inspect response?
9. Which proposed mechanisms could be deleted while still meeting the goals? Which source-specific loops should survive because they encode an algorithm rather than scheduling policy?
10. Can resource charts and task metrics distinguish exclusive measurements, sampling estimates, shared/runtime costs, and missing coverage? Does default monitoring stay cheap enough, and can it explain real CPU/memory bottlenecks?
11. Do PR 2's real adapters exercise admitted discovery, publication, and session reuse early enough to reshape the executor? Does the final named product query become correct/usable sooner, including when a coarse historical task is already running?
12. Are finite membership/coverage memory, shared execution retention, and the simple-task extension path proven centrally? Do the four PR boundaries remain clear and revertible as dependent suffixes, with one integrated cutover and on-schedule comparison retirement?

The review should produce corrections to the execution contract, an inventory of unknown task scopes, and a final four-PR stack outline. Implementation begins with measured baseline and durable dependency/rollback proofs; verification order does not require a sequence of partial production rollouts.

## 16 Implementation references

| Subject | Existing files |
| --- | --- |
| Operation contract and kinds | [background-work.md](background-work.md), [operations.go](../internal/archive/operations.go), [operations_api.go](../internal/archive/operations_api.go), [operation_kinds.go](../internal/archive/operation_kinds.go), [operation_journal.go](../internal/archive/operation_journal.go) |
| Rebuild and analysis orchestration | [incremental_recovery.go](../internal/archive/incremental_recovery.go), [incremental_sync.go](../internal/archive/incremental_sync.go), [capture_index_api.go](../internal/archive/capture_index_api.go) |
| Scheduled items and maintenance | [background.go](../internal/archive/background.go), [background_run.go](../internal/archive/background_run.go), [background_search.go](../internal/archive/background_search.go), [library.go](../internal/archive/library.go) |
| Models and preparation | [labels.go](../internal/archive/labels.go), [agent_events_run.go](../internal/archive/agent_events_run.go), [labels_prepare.go](../internal/archive/labels_prepare.go), [labels_items.go](../internal/archive/labels_items.go), [message_label_facts.go](../internal/archive/message_label_facts.go), [runner.go](../internal/labeler/runner.go), [local-labeler.md](local-labeler.md) |
| Authorship and relational state | [authorship.go](../internal/archive/authorship.go), [authorship_quick.go](../internal/archive/authorship_quick.go), [authorship_queue.go](../internal/archive/authorship_queue.go), [authorship_incremental.go](../internal/archive/authorship_incremental.go) |
| Incremental derivation and freshness | [tool_rollup_incremental.go](../internal/archive/tool_rollup_incremental.go), [parallelism.go](../internal/archive/parallelism.go), [findings.go](../internal/archive/findings.go), [fixes.go](../internal/archive/fixes.go), [freshness.go](../internal/archive/freshness.go) |
| Compatibility and resources | [catalog.go](../internal/archive/catalog.go), [resources.go](../internal/archive/resources.go), [resources_gpu.go](../internal/archive/resources_gpu.go), [resources_darwin.go](../internal/archive/resources_darwin.go), [incremental_metrics.go](../internal/archive/incremental_metrics.go), [power.go](../internal/archive/power.go), [operation_lease.go](../internal/archive/operation_lease.go) |
| Resource UI and task measurements | [status-panel.js](../internal/archive/assets/status-panel.js), [status-store.js](../internal/archive/assets/status-store.js), [work_measurement.go](../internal/archive/work_measurement.go), [api.go](../internal/archive/api.go) |
| Existing measurement foundations | [progress-estimates.md](progress-estimates.md), [dev.py](../tools/dev.py), [incremental_bench_test.go](../internal/archive/incremental_bench_test.go), [incremental_budget_probe_test.go](../internal/archive/incremental_budget_probe_test.go), [authorship_probe_test.go](../internal/archive/authorship_probe_test.go), [query-performance.md](query-performance.md), [thinking-performance.md](thinking-performance.md), [incremental-updates.md](performance/incremental-updates.md), [incremental-analysis.md](performance/incremental-analysis.md) |

Proposed implementation files should stay in `internal/archive` for task definitions, in-memory selection/execution, allocations, and inspection. Keep domain checkpoints with their current owners. A later host-resource component can connect the allocation contract to the existing broker runtime. Put the isolated workload runner and comparison/reporting helpers under `tools/`, and keep its private corpora/results under an explicitly selected experiment directory or `.context/`. Final names follow the reviewed contract; this plan does not create a new package hierarchy or a plugin system.
