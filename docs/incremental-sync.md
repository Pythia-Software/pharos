# Incremental conversation refresh

## Controls and freshness

Settings → Sync (`/settings/sync`) controls automatic refresh for this Mac's
enabled Claude, Codex, Conductor, and Antigravity sources. It starts **Off**.
The dedicated Settings page supports sidebar navigation, direct links, and
reloads without showing the other Settings sections. Choose one minute,
five minutes, fifteen minutes, thirty minutes, or a custom interval from one
minute to one day. The initial suggested interval is five minutes. Sources and
cadence are retained per host; an empty source selection means all supported,
enabled sources. **Check now** runs the same discovery, ingestion, projection,
and verification pipeline without enabling the timer.

The header's sync control (**Sync now**) runs **Check now** and shares its
busy state with automatic refresh, intentional capture/index, verification,
and repair. **Stop**, beside the running work in the drive panel, cancels it;
already committed groups remain usable. Runs do not overlap or queue missed
timer ticks. The next interval starts after a run finishes. A manual operation
cancels and waits for automatic work before taking the coordinator.

Automatic refresh reads live inputs but **does not preserve their raw source
evidence**. Freshness is the selected interval plus processing time. Pending
preservation is shown separately from indexing success. **Update library**,
in the drive panel, captures this Mac, indexes retained captures across Macs, reconciles deferred
library-wide work, and continues pending upgrades through the existing UI.
Its coordinator holds the pause across capture and index; independent capture
and index endpoints also pause automatic writes.

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
than silently rebuilding the entire library after every append.

Removed files do not delete retained conversation history. Removed Antigravity
children leave current group totals but remain searchable historically.
Reparenting has one current association; old captures cannot restore obsolete
membership over newer authority. A same-size/same-mtime rewrite may escape
discovery; independent audits and forced recovery are the backstops.
Antigravity has no atomic snapshot spanning its files: dependency movement
defers verification or leaves a group eligible for the next refresh.

## Repair and verification

The disk menu, Sync settings, and integrity issues expose:

- **Full recapture & re-index…** freshly copies available selected local
  evidence, then forces parser, search, ledger, and supported derivation work
  through selected captures on every retained host. Other Macs are re-indexed
  from captures, not remotely recaptured. Deleted Conductor sessions can be
  recovered from superseded database generations. Last good evidence remains
  retained until replacement is verified. Copy/index estimates use prior
  measured throughput; unknown measurements are not guessed.
  Byte-identical file recaptures do not create superseded history entries;
  genuinely changed file versions still preserve the prior evidence.
- **Rebuild indexes from retained messages…** rebuilds search, conversation
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

The verifier freezes evidence and creates an isolated reference catalog,
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

A confirmed mismatch creates a deduplicated persistent issue. Diagnostics
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
| `POST /api/sync/check`, `POST /api/sync/stop` | Run now / request cancellation. |
| `GET /api/sync/history`, `GET /api/sync/issues` | Measured samples / local diagnostic evidence. |
| `GET /api/sync/recovery?estimate=true&source=codex` | Retained size/group counts and measured copy/index estimate. |
| `POST /api/sync/recovery` | `{"mode":"full","sources":["codex"]}`, `{"mode":"retained"}`, or `{"resume":"JOB_ID"}`. |
| `POST /api/library/update` | Coordinated ordinary capture/index/deferred reconciliation. |
| `POST /api/sync/verify` | `{"all":true,"sources":["codex"]}`; omitted source scope means all inputs. |
| `GET /api/activity` | Progress, interruption, failures and verification summaries by returned run ID. |

## Measurement and regression gates

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
PHAROS_INCREMENTAL_RESOURCE_GATES=1 go test ./internal/archive \
  -run '^TestIncrementalScheduledResourceBudgets$' -count=1 -v
go test ./internal/archive -run '^$' -bench '^BenchmarkIncremental' \
  -benchtime=3x -benchmem -count=1 -cpuprofile=/tmp/pharos-cpu.pprof \
  -memprofile=/tmp/pharos-memory.pprof -o /tmp/pharos-benchmark.test
PHAROS_BENCH_ROOT=/Volumes/YOUR_LIBRARY_SSD/scratch \
  go test ./internal/archive -run '^$' -bench '^BenchmarkIncremental' \
  -benchtime=3x -benchmem -count=1
go tool pprof -top /tmp/pharos-benchmark.test /tmp/pharos-cpu.pprof
node --test tests/incremental-sync-ui.test.cjs tests/combo-button-ui.test.cjs
```

`PHAROS_BENCH_ROOT` must already exist. Only temporary benchmark children are
created/deleted there. Browser tests use the existing Playwright test harness
in `.context/browser-tests` and installed Chrome, or Playwright's Chromium.

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
deliberately heavier than automatic refresh.

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
modified, and automatic refresh remained Off.

After integrating master's activity-popover estimates, all six Antigravity
warm checks passed again against a rebuilt binary. CLI checks after its
process-local log cache warmed took 0.370 / 0.374 seconds, including audits;
the first CLI check after server restart took 3.569 seconds. These are warm
catalog checks, not new cold imports. Completion estimates retain separate
histories for source indexing, captured-input indexing, automatic refresh,
verification, and coordinated library updates, so a quick refresh cannot
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
