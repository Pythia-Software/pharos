# Index changes

Pharos keeps a library current with two verbs:

- **Capture** copies this Mac's source files into the library, so they are
  kept even if the originals are deleted.
- **Index** reads conversations into the library so they are searchable and
  analyzed.

**Index changes** reads live sources by default. Experimental retained-input
acquisition is opt-in; the source matrix below describes its limits. **Capture
& index** captures this Mac, indexes captures from every Mac, and finishes
pending library upgrades.

## Controls and freshness

The header button beside the library disk runs **Index changes**. Settings →
Background updates (`/settings/sync`) runs it on a schedule, the
**Frequently** cadence, for this Mac's enabled Claude, Codex, Conductor, and
Antigravity sources. Background updates are **On** by default (a recovered
library starts with them off); **Pause for 1 hour** and **Only when I ask**
stop them. The interval is two, five (the default), fifteen or thirty
minutes. The schedule is retained per host; an empty source selection
(`sync_settings.sources_json`, no longer shown) means all supported, enabled
sources. **Update everything now** (and the header button) runs that
incremental pipeline, then refreshes GitHub repository identities,
library-wide identity links, local Git integration, tool rollups, Human
Words, and a full findings pass. The manual run remains active until those
stages finish. GitHub is optional: missing or signed-out `gh` leaves existing
repository data usable. A scheduled run leaves these analyses pending
(`sync_deferred`); the background schedule then runs each on its own cadence,
Frequently, Hourly or Daily by its measured cost
([background updates](background-work.md#11-background-updates)).

The page opens with the latest run's conversation count, elapsed time, CPU
time, and observed RAM peak, followed by a table of the five most recent
Index changes runs. Expand the history to see up to 200 retained runs.
Each row identifies its sources and their conversation counts; **Details**
shows per-source message counts, phase timings, and verification results.
Manual history records `trigger: "manual"` and elapsed/process-wide CPU time
for each analysis stage; scheduled history records `trigger: "automatic"`.
Counts describe whole indexed conversations, not just newly added
messages. Times use compact durations and relative timestamps; hover a
timestamp for the exact date and time. Performance distributions,
verification coverage, and repair controls are in expandable sections.

The page's **Capture & index** section runs the same action as the drive
panel's **Capture & index** button. **Capture or index separately** exposes
**Capture this Mac** and **Index captured files** when only one step is needed.
Their progress, errors, and index **Stop** control appear here. All of these
share one busy state. Settings → Sources keeps discovery, configuration,
and source status, with a link here instead of its own capture/index controls.

The header's **Index changes** shares its busy state with automatic indexing,
Capture & index, verification, and repair. **Stop**, beside the running work
in the drive panel, cancels it; already committed groups remain usable. Runs
do not overlap or queue missed timer ticks. The next interval starts after a
run finishes. A capture or index started by hand cancels and waits for
automatic work before taking the coordinator.

With acquisition enabled, Index changes reports preservation and parsing as separate phases. Freshness
is the selected interval plus processing time. Preservation status uses accepted
copy identities and committed input inventories, independently of capture/index
timestamp order. Historical copies without an evidence association stay visible
as legacy gaps, including after an unchanged index. **Capture & index** captures this
Mac, indexes retained captures across Macs, reconciles deferred library-wide
work, and continues pending upgrades through the existing UI. Its coordinator
holds the pause across capture and index; independent capture and index
endpoints also pause automatic writes.

## Source acquisition rollout

| Source | Index changes / API / CLI ingest |
| --- | --- |
| Claude | Live by default. `retained_input_capture = true` retains files, subagents and naming indexes before planning and parsing. |
| Codex | Live by default. `retained_input_capture = true` enables file-only acquisition. Sources with SQLite naming stores require `sqlite_record_capture = true` for transactional row export with a committed dependency group, without native backup per tick. |
| Canonical, TL1 exports, ChatGPT exports | Live by default. `retained_input_capture = true` retains export files before parsing, with existing automatic-indexing eligibility. |
| Conductor | `sqlite_record_capture = true` uses the existing candidate session exporter and periodic full reconciliation. Default live ingestion remains pending the hot 11 GB validation. |
| TL1 and Antigravity | `sqlite_record_capture = true` enables bounded logical database/file groups. Default live ingestion remains a preservation gap pending validation of incremental costs. |

The options belong inside a source's `[sources.options]` table. Filesystem
acquisition remains experimental: it keeps complete immutable inventories and
has no collector, so growing transcripts and inventory entry counts can grow
without a bound. Do not enable it on production timers until retention and
backup file counts have been validated for that library. Default indexing
creates no such inventories. No options are enabled by an upgrade.

This is a staged implementation, not completion of the all-source rollout or
historical rebuild. SQLite group acquisition reuses row chunks and dependency
objects when inputs are unchanged, and scans changed databases within the
[documented budgets](sqlite-records.md). It still scans all rows of a changed
TL1/Antigravity/Codex metadata database; candidate-level acquisition and production performance
validation remain required before making that path the default. No source
configuration is changed automatically. Unsupported or failed acquisition after
a source's cutover returns an explicit pending/error result, without parsing live
inputs as a fallback.

With filesystem acquisition enabled, inventories are published under
`captures/<host>/<source>/inputs/<revision>/manifest.json`. They reference immutable
hard links to the acquired files, or flushed copies on filesystems such as
exFAT that do not support hard links. Copies are streamed, verified against the
retained bytes, and retain their modification times. The free-space reserve
and acquisition allowances include these additional bytes. Capture replaces
latest files with new inodes, so another capture, source deletion, or a parser upgrade cannot change an active
reader's inventory. These revisions are evidence and are not automatically
collected. Append growth therefore retains earlier complete file revisions;
unchanged files share storage where links are available. Local and cloud
backups preserve these links when supported; cloud archives still enforce their
200,000-path restore limit. Default captured-file indexes and native snapshot
readers instead use temporary leases, with verified copies when links are
unavailable. Their raw identities do not claim an immutable inventory receipt.
Leases are released after parsing and keep existing rotation rules.
Ordinary captured-file indexing skips missing or resized entries and reports
them in `unavailable_capture_inputs`, while continuing with healthy files and
surviving snapshot generations. A partial lease does not certify a durable
inventory. Acquisition and verification still require complete retained inputs.
Logical SQLite readers use the durable revision/pin contract from handoff 1.

The shared `Catalog.acquireRetainedInput` boundary is used by ingestion, service
preparation and CLI ingestion. Metadata backfills use existing captured inputs
when available, otherwise same-host live files; they never create enrichment
captures. Offline evidence-only rebuilds cannot use that live fallback.
Plans built over live files
are discarded after acquisition and rebuilt against the retained reader. A
whole-source logical group advances its parts checkpoint only after all of its
records commit. With acquisition disabled, verification uses bounded temporary
inputs as before and does not certify durable preservation. SQLite row export
is never enabled merely to run an automatic audit. Logical group verification compares each selected workspace against its pinned
revision. Conductor verification acquires a bounded durable session even when
normal live indexing has not opted into enforcement.

The ingestion coordinator is acquired only by entry points. Acquisition takes
the nonblocking host capture flock, publishes evidence, and releases the flock
before parsing, catalog transactions or verification. Explicit capture indexing
takes the index lock first, then briefly takes the host flock to lease its exact
inventory. Acquisition never recursively takes the ingestion coordinator. No
upload runs under these locks. A busy host capture defers automatic work until
a later tick, without consuming a retry or advancing success/checkpoint state;
manual callers receive the existing capture-in-progress explanation.

`source_input_states` separates acquisition time/revision, indexed inventory,
pending newer inputs and acquisition errors. `copy_evidence` identifies accepted
catalog copies transactionally; a no-op acquisition does not manufacture evidence
for older catalog rows. Source status and MCP archive status expose both this
operational summary and legacy copy counts. Interrupted acquisition leaves earlier
content and checkpoints intact. Committed evidence can be indexed offline after
an interruption using **Index captured files** or `pharos index`.

## Changed-unit ingestion

Changed inputs use the existing full-record parser and copy-authority writer,
not a durable byte cursor or another ingestion implementation:

- Claude reparses the complete root/subagent group; Codex reparses a rollout.
  Discovery uses size/mtime and extractor/generation state in a single walk.
- Conductor compares database/WAL identities before session metadata signals.
  Changed sessions are read in bounded, cancellable snapshots, rather than
  holding the source read transaction across all catalog writes. Declared
  native aliases resolve regardless of which provider arrives first.
- Antigravity app/CLI/IDE directories are independent configured sources.
  Group membership, chosen transcript, summary/annotations, CLI evidence, and
  request database/WAL metadata can invalidate a group without a new message.
  Missing optional dependencies differ from present but unreadable ones.
  Moving or disappearing groups are retried on a later pass without marking
  their parts current; healthy sibling groups still ingest. Group-local read
  errors are reported after healthy siblings have been processed.

Writes skip identical messages, change only affected FTS/trigram rows, share
token accounting, and reuse unchanged ledger derivations. Changed workspaces
publish their immediate Library projections in a measured batch. Identity,
Git, Tools rollups, authorship, and findings work is marked pending rather
than silently rebuilding the entire library after every append. Scheduled
runs stop there. Manual Index changes finishes pending analyses even when
the source scan finds no new changes, so it can catch up after automatic
indexing or an interrupted refresh. Capture & index also reconciles pending
work. Both keep dirty generations until the corresponding stage succeeds.

Removed files do not delete retained conversation history. Removed Antigravity
children leave current group totals but remain searchable historically.
Reparenting has one current association; old captures cannot restore obsolete
membership over newer authority. A same-size/same-mtime rewrite may escape
discovery; independent audits and forced recovery are the backstops.
Antigravity has no atomic snapshot spanning its files: dependency movement
defers verification or leaves a group eligible for the next run.

## Repair and verification

Settings → Background updates and integrity issues expose:

- **Full recapture & reindex…** freshly copies available selected local
  evidence, then forces parser, search, ledger, and supported derivation work
  through selected captures on every retained host. Other Macs are re-indexed
  from captures, not remotely recaptured. Deleted Conductor sessions can be
  recovered from superseded database generations. Last good evidence remains
  retained until replacement is verified. Copy/index estimates use prior
  measured throughput; unknown measurements are not guessed.
  Byte-identical file recaptures do not create superseded history entries;
  genuinely changed file versions still preserve the prior evidence.
- **Reindex from stored messages…** reindexes search, conversation
  documents, sessions, token usage, and tool ledgers without original sources.
  It bypasses derivation fingerprints and cannot recover missing messages,
  original files, or source-only metadata. History, preferences, identity
  links, and user-maintained findings are not reset.
- **Resume repair** reuses the job's generation and committed progress,
  including verified captures. It does not start another force generation.
- **Verify all retained inputs** visits configured live inputs and captures
  across hosts, including the newest historical Conductor generation holding
  each deleted session. Library/source scopes are supported. Targeted
  conversation/workspace repair remains the design's follow-up, not v1.

Every completed automatic pass attempts one least-recently verified stable
unit, including skipped and discoverable-but-unindexed units. JSONL/group
inputs younger than thirty seconds defer. Automatic audits have a five-second
wall deadline and 16 MiB evidence/comparison budget; **Verify all** has no
per-unit size or wall-time budget and remains cancellable with **Stop**.
Moving/unreadable inputs report deferred or
read-error coverage, never an invented pass. Resource telemetry and opt-in
regression gates are not hard per-process memory or CPU quotas.

The verifier uses a pinned retained revision and creates an isolated reference catalog,
seeded with identity/authority/retained-copy context and the catalog's global
repository lookup metadata for cross-repository tool paths. Context remains
subject to the comparison memory budget. It bypasses incremental
skip decisions and compares normalized messages, ordering, search coverage
and representative queries, usage, sessions/model requests, tool ledgers,
conversation documents, group totals, and immediate workspace metrics.
Volatile timestamps, internal search row IDs, and deferred library-wide
analyses are excluded. Comparison reads do not modify retained message data.
Antigravity CLI audits freshly stream run logs to select only evidence naming
members of the audited group, rather than copying every unrelated run log into
the bounded snapshot. Frozen evidence parsing bypasses the live log cache;
live log discovery uses a bounded cache and a cancellable chunked reader that
handles long lines and conversation IDs crossing chunk boundaries.

A confirmed mismatch creates a deduplicated persistent issue. While one is
open, the drive dot in the header is red and pulses, and the drive panel lists
it under **Needs attention** with **Review…**, which opens Settings → Capture
& index, where its details and repair actions are. Diagnostics
record field categories and signatures without transcript text. Repair alone
does not dismiss an issue: the repaired unit must pass verification. Sampling
does not certify the entire library, and a shared parser bug can still agree
with the reference; provider fixtures remain necessary.

## API

Existing authentication applies to all endpoints. POST bodies are JSON.

| Endpoint | Purpose / body |
| --- | --- |
| `GET /api/sources/{name}/changes` | Read-only changed-unit discovery; reports completeness, errors, units and check duration; never advances indexed state. |
| `GET /api/sync/status` | Settings, shared busy state, preservation boundary, history/statistics, coverage, issues and deferred work. |
| `GET /api/sync/settings`, `POST /api/sync/settings` | `{"enabled":true,"interval_seconds":300,"sources":["codex"]}`; 60–86400 seconds. |
| `POST /api/sync/check`, `POST /api/sync/stop` | Manual incremental index plus expensive refresh stages / request cancellation. |
| `GET /api/sync/history`, `GET /api/sync/issues` | Measured samples / local diagnostic evidence. |
| `GET /api/sync/recovery?estimate=true&source=codex` | Retained size/group counts and measured copy/index estimate. |
| `POST /api/sync/recovery` | `{"mode":"full","sources":["codex"]}`, `{"mode":"retained"}`, or `{"resume":"JOB_ID"}`. |
| `POST /api/library/update` | Coordinated ordinary capture/index/deferred reconciliation. |
| `POST /api/sync/verify` | `{"all":true,"sources":["codex"]}`; omitted source scope means all inputs. |
| `GET /api/activity` | Progress, interruption, failures and verification summaries by returned run ID. |

## Measurement and regression gates

Frequently polled source status reports only the latest acquisition and whether
it was indexed. Default live sources have no acquisition warning. These reads
do not count historical conversation sightings or join per-copy evidence;
detailed historical coverage is inspected through an explicit preservation plan.

History samples expose `finished_at`, `sources` (attempted sources), and
`source_results` (the per-source ingest results). Older samples receive their
completion timestamp from the history record; their source names can be
recovered from phase timings, but missing per-source counts are not inferred.

Run history separates unchanged, changed, failed/interrupted, and cold-start
work, and includes audit/projection costs and phase timings. CPU is
process-wide including completed child processes and concurrent Pharos work,
relative to one core. RSS/heap peaks are sampled; sub-second peaks can be
missed. Allocations are process-wide, and OS I/O counters are **block counts,
not measured bytes**. Duty-cycle estimates are not a power-consumption SLA.

Reproduce workloads with scratch catalogs only:

```sh
go test ./...
go test ./internal/archive -run '^TestIncrementalWriterRowBudget$' -count=1
PHAROS_INCREMENTAL_RESOURCE_GATES=1 go test -tags probe ./internal/archive \
  -run '^TestIncrementalScheduledResourceBudgets$' -count=1 -v
go test ./internal/archive -run '^$' -bench '^BenchmarkIncremental' \
  -benchtime=3x -benchmem -count=1 -cpuprofile=/tmp/pharos-cpu.pprof \
  -memprofile=/tmp/pharos-memory.pprof -o /tmp/pharos-benchmark.test
PHAROS_BENCH_ROOT=/Volumes/YOUR_LIBRARY_SSD/scratch \
  go test ./internal/archive -run '^$' -bench '^BenchmarkIncremental' \
  -benchtime=3x -benchmem -count=1
go tool pprof -top /tmp/pharos-benchmark.test /tmp/pharos-cpu.pprof
npm --prefix web run test:browser -- --test-name-pattern='Capture|Sources'
```

`PHAROS_BENCH_ROOT` must already exist. Only temporary benchmark children are
created/deleted there. The browser suite uses web's `playwright-core` with the
installed Chrome; it checks only that the Sources and Capture & index pages
render and keep their layout. Test `sync.js` logic in node.

The default deterministic writer gate allows at most 40 affected rows for an
unchanged 2,000-message record and 80 for one append (including metadata and
trigger writes). Measured appends use 45 rows. Evidence/comparison size limits
bound audit acquisition. Physical byte-I/O ceilings are not inferred from OS
block counts.

Opt-in scheduled resource gates require a relatively idle host and successful
verification. For 200-message unchanged/append/metadata/first-run workloads:
3 seconds wall, 3 seconds CPU, 64 MiB allocated, 128 MiB observed RSS growth.
Eight active groups allow 128 MiB allocated. A 4,000-message group allows
10 seconds wall/CPU, 600 MiB allocated, 512 MiB observed RSS growth. These are
generous regression ceilings, not latency promises; ordinary correctness tests
do not enforce noisy machine-wide timing thresholds.

Local profiling on October 1, 2026 used an Apple M3 Ultra, an internal SSD,
the library SSD, and a separately served replica with about 5.4 million
messages. The replica tests exercised append/no-op audits, Stop/resume with
the same generation, fresh capture hashes, deliberate trigram corruption,
source-independent repair, captured-input verification, and preservation of
all original conversation IDs and preferences. Production catalog and service
were not modified. Synthetic results and their small sample size must not be
generalized to every provider or library. Profiling removed whole-conversation
search rewrites, repeated unchanged-message SQL, unindexed audit scope scans,
and large serialized comparison keys; library-wide reconciliation remains
deliberately heavier than automatic indexing.

The final three-iteration, profiled synthetic runs measured:

| Workload | Internal SSD | Library SSD |
| --- | ---: | ---: |
| Scheduled unchanged, 200 messages, including audit | 126 ms | 107 ms |
| Scheduled append, 200 messages, including audit/projections | 134 ms | 122 ms |
| Scheduled metadata-only change | 141 ms | 112 ms |
| Eight active groups | 186 ms | 162 ms |
| One 4,000-message group | 949 ms | 1,417 ms |
| First scheduled ingest, 200 messages | 204 ms | 160 ms |
| Audit only, 200 messages | 119 ms | 103 ms |
| Writer append, 2,000 messages, 45 affected rows | 24 ms | 35 ms |

These are small-sample means with profiling enabled; the replica was finishing
maintenance concurrently. The comparable earlier synthetic writer append was
153 ms; the original review's 7–8-second Claude append used a different, much
larger real transcript and is not a like-for-like comparison. Scheduled
4,000-message allocation fell from about 397 MB to 302 MB after audit
comparison optimization. Replica profiling also identified repeated alias
lookups scanning conversations; `conversations_native_alias_idx` now supplies
indexed account/native-ID lookup, with an EXPLAIN-plan regression test.

The actual one-minute timer indexed an appended validation message against
the large replica in 754 ms, including a passed 523 ms audit. A separate live
Codex run processed eight changed groups (5,059 messages in those groups) in
8.72 seconds, including projections and a passed 424 ms audit. These workloads
are not comparable to each other or to the synthetic writer-only benchmark.

Read-only discovery against this Mac's real sources measured Conductor:
4,377 sessions in 7.32 seconds; Claude: 1,985 groups in 500 ms; Codex:
4,757 rollouts in 1.31 seconds. Discovery parses no message bodies and advances
no indexed state. Conductor reported 4,373 changed sessions in this replica,
so discovery alone is not a cold-ingest or steady-state cadence measurement.

Additional real-provider canaries used fresh scratch catalogs on the library
SSD and read-only live provider inputs. After fixing group-scoped CLI audit
evidence acquisition, first-import results were:

| Provider | First import | Indexed conversations / messages | Two warm passes, including audit |
| --- | ---: | ---: | ---: |
| Antigravity app | 0.414 s | 1 / 21 | 0.292 / 0.308 s |
| Antigravity CLI | 19.737 s | 502 / 2,510 | 0.507 / 0.486 s |
| Conductor | 54 min 14 s | 4,380 / 1,763,154 | 2.914 / 1.125 s |

All six Antigravity audits passed; those warm passes indexed no changes. CLI input included
76 MB of run logs. Warm allocated bytes fell from about 157 MB to 72 MB after
bounding the cache without thrashing and streaming log selection; these are
total allocations per pass, not resident memory. The previous CLI runs had
deferred audits because unrelated logs exhausted the 16 MiB evidence budget,
so their shorter runtimes are not successful-verification comparisons. The
app is a one-conversation sample, and no real IDE source was available. These
single-machine, small-sample results are not provider-wide latency promises;
the fixed canaries ran concurrently with a large Conductor import and final
correctness/race tests. No production service/catalog or live input file was
modified, and automatic indexing remained Off.

After integrating master's activity-popover estimates, all six Antigravity
warm checks passed again against a rebuilt binary. CLI checks after its
process-local log cache warmed took 0.370 / 0.374 seconds, including audits;
the first CLI check after server restart took 3.569 seconds. These are warm
catalog checks, not new cold imports. Completion estimates retain separate
histories for source indexing, captured-input indexing, automatic indexing,
verification, and Capture & index, so a quick Index changes run cannot
become an unrelated operation's historical estimate.

Conductor's roughly 9 GB real database needed a full, uninterrupted first
import. Observation was extended without restarting ingestion; the same run
committed all 4,380 conversations and passed its sampled audit. Its first
warm check processed four changed groups (875 messages); the second indexed
no changes. Cold observed RSS peaked at about 2.2 GiB, and cumulative allocated
bytes were about 728 GB, not simultaneous RAM use. Discovery/parsing took
about 1,006 seconds, committed writes 2,185 seconds, and immediate projections
60 seconds. A ten-second CPU profile attributed about 55% of samples to
syscalls and about 62% cumulatively to the ingest writer. Cold throughput
remains expensive and is not a one-minute freshness promise; the timer is Off
by default and missed checks do not queue.

Additional sampling caught an audit-isolation false positive: the reference
catalog lacked another repository's checkout metadata and disagreed only on
cross-repository tool-path attribution. The verifier now seeds that global
lookup context, while independently rebuilding the ledger. A regression also
corrupts tool status to ensure real ledger errors still fail. The original
real unit passed after this fix without rewriting retained messages, resolving
the false alert. The final rebuilt binary passed all nine follow-up sampled
audits: Conductor's changed/no-op/no-op checks took 2.628 / 1.247 / 0.560
seconds, and the CLI's two warmed-cache checks took 0.409 / 0.368 seconds.
These final runs overlapped correctness/race tests. One earlier bounded
Conductor audit correctly deferred a large session for manual verification;
that is not a passed audit. Sampling does not verify every retained session.
All scratch timers were restored to Off and their servers stopped; production
remained untouched.

Sync/Update browser tests pass (7/7), as do the Go suite, scoped race tests,
`go vet`, deterministic row gates, and all six opt-in scheduled resource
workloads. After integrating master through `c3e1bbc`, the broader browser
suite reports 87/98 passing versus 87/97 on an isolated checkout of that exact
master, both using Node 20.19.1. The same ten stable tests fail in both: Carbon,
Findings empty state, onboarding summary, off-host Sources, Sources toolbar,
Preferences, Library search explanation, MCP mocks, Navigator, and linked PRs.
The additional failure is master's new completion-estimate browser test:
its layout measurement intermittently sees a detached element and returns a
null bounding box. The identical failure reproduces on isolated master with
the same Node/browser versions; the final focused run passes all seven tests.
Thus every observed failure reproduces on master, with no introduced failure
identified. Some inherited expectations predate master's Settings navigation
changes. No unrelated browser behavior was changed to make that suite green.
An initial concurrent full/race Go run hit a capture-lock teardown timing
assertion; the final sequential `go test ./...` rerun passed after all code
edits stopped. Scoped race tests, `go vet`, focused UI tests, bundle/format
checks, and all six opt-in resource gates also passed on the final code.
