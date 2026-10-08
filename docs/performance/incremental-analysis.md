# Incremental tool and findings analysis

Measured on October 8, 2026. Tool updates exceed the 10× target on the tested
small updates. Findings improve substantially but **do not reach 10×**.
Scheduled expensive analyses remain disabled. Manual Index changes refreshes
all expensive stages even when indexing finds no new source activity.

## Workload and measurement

The consistent SQLite backup has 33,881 workspaces, 30,264 conversations,
1,408,312 tool calls, 272,440 cube rows, 61,184 daily tool rows, and 4,096
suppressed mirror workspaces. Its main file is 68,581,720,064 bytes (63.87 GiB).
The baseline full pass produced 189 findings. The host is an Apple M3 Ultra
with 96 GiB RAM.

The production catalog was opened read-only for a SQLite backup under an
explicit read transaction; the main file was not copied independently of its
WAL. All writes and measurements use scratch replicas and isolated benchmark
processes. APFS clones were made only after the backup completed. The user's
app, native transcripts, settings, and installed binaries were not changed.

The update series runs a no-op, a corrected call (+1 result token and +10
carried tokens), an appended call, corrections in eight recent conversations,
and a final no-op. Those conversations contain 15, 240, 465, 16, 10, 162, 65,
and 68 calls; their seven distinct workspaces contain 15–556 calls. The one-call
workload selects the 15-call workspace. Results establish scaling for this
workload, not a universal upper bound for a huge active workspace.

The original implementation's first and second full passes used identical
original inputs. The second is the warm baseline. Updated measurements use
both a fresh clone of the original inputs and a previously built replica. The
latter has a few controlled corrections/appends from earlier experiments.
Cache warmup is unmeasured in the warm update series. A new process may reload
persisted JSON; a new feature version builds it from raw evidence.

CPU seconds, Go allocation deltas, malloc counts, and end-of-stage RSS are
process-wide, isolated from other Pharos processes. Other user workloads still
share the machine, and filesystem cache state was not cleared or controlled.
These are individual observations, not repeated statistical estimates. Never
compare cold baseline wall time to warm optimized wall time as a clean speedup.
RSS is not sampled peak or memory attributable solely to one stage. macOS block
counters returned zero: they are not I/O bytes, and no byte savings are claimed.

## Baseline

| Stage | First wall / CPU seconds | Warm wall / CPU seconds | Allocated bytes per warm pass | Warm end RSS |
| --- | ---: | ---: | ---: | ---: |
| Tools | 57.595 / 31.466 | 38.720 / 34.008 | 6.905 GB | 4.113 GB |
| Findings | 275.652 / 74.518 | 50.739 / 36.887 | 5.482 GB | 4.133 GB |

The original tool writer held the catalog for 7.237 and 7.439 seconds. Baseline
profiles show broad SQLite scans, column conversion, shell parsing, and
allocation. The original failure, call-group, and heavy-output plans scan by
start time or result tokens over the entire window.

## Warm incremental observations

These timings precede the follow-up fix for the first-user prompt cache.
That fix keeps help-conversation IDs out of the feature query and its version:
all top-level prompts in the window are cached by message revision, and the
current help set is filtered in Go. Adding help activity now writes only new or
changed prompt partitions. An INSERT/UPDATE/DELETE audit regression verifies
this behavior and reference parity; the timings below have not been rerun.

The final `final-verified.log` series starts with the original inputs, builds
`features-v2` caches, and applies these controlled updates. The no-op before
updates has the same authoritative inputs as the warm baseline.

| Workload | Tools wall / CPU seconds | Tool allocation | Findings wall / CPU seconds | Findings allocation |
| --- | ---: | ---: | ---: | ---: |
| No-op | 0.004045 / 0.000359 | 7.2 KB | 14.790 / 16.491 | 3.539 GB |
| Corrected call | 0.137020 / 0.160514 | 23.220 MB | 12.790 / 15.615 | 3.538 GB |
| Appended call | 0.137129 / 0.159064 | 23.219 MB | 13.349 / 15.650 | 3.539 GB |
| Eight conversations | 0.176637 / 0.198566 | 31.028 MB | 13.162 / 15.812 | 3.546 GB |
| Final no-op | 0.000418 / 0.000670 | 7.3 KB | 11.493 / 14.346 | 3.537 GB |

Tool small updates improve wall time by about 219–283× against the warm full
baseline, with about 223–297× less allocation. Findings improve wall time by
roughly 3.4–4.4× and CPU by about 2.2–2.6×; their allocation drops about 35%. Warm process
end RSS is approximately 4.75 GB, higher than the baseline because immutable
features remain resident. Reducing repeated work does not imply reduced RSS.
Earlier warm trials on this active machine observed tool updates of 0.15–0.74
seconds and findings of 12–19 seconds; the raw logs preserve that variation.

Tool publication metadata measured 3–16 ms for the final updates. It is
recorded before commit, so it is not the complete writer hold time. No update
emitted the existing one-second writer warning. Full publications held 2.658
seconds initially and 2.473 seconds for the final forced reference. No-op tool reads do not publish; the harness reports `not-published`.

On a fresh clone of the original inputs, the final-code first build measured:

| Stage | Wall / CPU seconds | Allocated bytes | End RSS |
| --- | ---: | ---: | ---: |
| Tools | 53.492 / 32.403 | 7.034 GB | 0.392 GB |
| Findings | 81.766 / 57.406 | 7.413 GB | 4.747 GB |

The first tool publication held 2.658 seconds (pre-commit metadata: 2.179
seconds). First-build tools remain expensive, and feature construction allocates
more than the original findings pass. These observations are not controlled
cold-cache speedups. The final original-input update series and parity receipt
are retained in `final-verified.log`.

## What changed and what remains expensive

Tool dirtiness is durable and recorded transactionally. Updates rebuild dirty
workspace cube partitions, compare mirror representatives, and replace only
those partitions and affected daily summaries. Construction and pricing happen
in a coherent WAL snapshot before taking the writer. Completion metadata and
rows publish atomically; newer dirty tokens survive. Mirror selection still
reads workspace/link metadata globally, and daily aggregation reads all cube
partitions on affected days. The measured speedup does not imply that every
read is bounded by the changed call count. Competing publication is
rejected; busy publication retries reuse temporary data. Full fallback handles
upgrades, broad attribution/pricing changes, timezone/day rollover, and repair.

Findings cache conversation inputs and static call classifications, with
independent prompt-message revisions. Each refresh still performs full candidate
discovery, gates, backtests, persistence, user state handling, fixed intervention
plans, savings, and regression decisions. Query arguments and rule/window
versions invalidate features. Context and inputs share one read snapshot.
Corrupt JSON is rebuilt from raw evidence on reload.

Changed-feature query plans use `tool_calls_conversation_idx`; cached tokens
use the feature partition primary key. Unchanged partitions are neither
rewritten nor decoded again in a warm process. These eliminate broad raw-tool
reads for small updates, but do not eliminate global context loading or full
candidate evaluation. Warm failure detection still takes around 3–4 seconds,
drift about 3 seconds, and remaining detectors, backtests, and saving several
more. The allocation profile still puts failure candidate/observation maps
among the largest consumers. Future work should compact exposure/observation
structures and cache recovery inputs with complete invalidation, then profile
context, statistics, and persistence separately. Skipping statistical gates or
user plans would change the contract and was not used to meet a timing target.

The combined optimized CPU/heap profiles include warmup, full-reference work,
and large canonical output comparison; their totals are not per-refresh costs.
Use the stage meter rows above for those comparisons. In particular, parity
comparison adds large transient allocations unrelated to normal analysis.

## Correctness and policy

Full-reference parity passed on the large replica after controlled updates.
Canonical comparisons cover the cube, daily tool rows, mirror suppression,
findings, daily finding observations, aliases, actions, carts, and intervention
state. Only `updated_at` is excluded; JSON numbers use the existing 1e-7 relative
(or absolute near zero) tolerance. Evidence and user state remain part of the
comparison.

The old implementation iterated maps when selecting capped evidence and tied
failure-family candidates. Those choices could differ between two full passes.
Sorted traversal and identity tie-breaks now make the reference reproducible;
the existing preference for the candidate affecting more conversations stays.
Parity means equality against that full reference, not byte-for-byte equality
with an arbitrary old run's nondeterministic evidence selection. A separate
comparison with the original implementation also caught program-key whitespace
normalization introduced by converting typed SQL scans to maps. Raw group keys
now retain their original strings, with a regression test for exposure counts.
After that fix, comparison on identical original inputs found all 189 finding
IDs and their statistical/card/fact fields equal to the legacy baseline, all
8,410 daily observations equal, and all 526 aliases equal. Sixteen evidence
arrays differ because of stable selection among tied examples. All 272,440
cube rows, 61,184 daily tool rows, and 4,096 mirror rows also match the original
baseline (`legacy-final-tool-comparison.json`). Evidence is
still compared in the full-reference audit. The read-only legacy comparison
receipt is `legacy-final-comparison.json`.

Tests cover additions, corrections, reclassification, timestamp movement,
pruning, repository names, session depth, mirror/representative changes,
conversation moves, pricing, rollover, cancellation, competing publishers,
new dirtiness during construction, bounded partition replacement, prompt-only
invalidation, cache corruption, and unchanged-partition reuse. Manual/automatic
pipeline tests retain the prerequisite policy and independent Human Words
invalidation. Final validation passed:

- `go test ./...` (archive package: 139.121 seconds).
- Scoped race tests for partitions, publication/retry, cancellation, prompt
  changes, cache recovery, concurrent revisions, and manual/automatic policy
  (127.999 seconds).
- The final large replica full-reference audit (304.69 seconds including builds,
  updates, and comparison); the legacy output audits described above.
- Five browser tests passed for the integrated manual-indexing prerequisite.
  An initial header-spinner timing failure passed on isolated retry and the
  subsequent full browser rerun. UI code has not changed since that rerun.
- `git diff --check`; prerequisite source files still match the final manama
  diff, with additional local documentation only in Findings and Tools.

These results support a future coalesced tool refresh trial around every five
minutes after changed input, with a separate budget for broad/full rebuilds.
They do not justify full findings after every scheduled scan. Keep findings
manual for now; a future opt-in trial should be no more frequent than hourly,
require changed inputs, and prevent overlapping passes. First-build/window
rollover cost and resident memory need their own budget. No scheduling policy
was enabled by this implementation.

## Reproduction and artifacts

The opt-in harness rejects the installed production catalog path, including
symlinks. Supply only a consistent scratch backup:

```sh
go test -c ./internal/archive -o .context/performance/analysis.test
PHAROS_ANALYSIS_REPLICA="$PWD/.context/performance/scratch.sqlite3" \
PHAROS_ANALYSIS_INCREMENTAL=1 PHAROS_ANALYSIS_PROFILE=1 \
PHAROS_ANALYSIS_PARITY=1 \
.context/performance/analysis.test -test.run '^TestAnalysisReplica$' \
  -test.v -test.timeout 30m \
  -test.cpuprofile .context/performance/analysis.cpu.pprof \
  -test.memprofile .context/performance/analysis.mem.pprof
```

Add `PHAROS_ANALYSIS_WARM=1` for an unmeasured cache warmup followed by small
updates. Omitting incremental mode forces tool rebuilding for the two baseline
passes; reproduce the original findings baseline with the original source/test
binary, since current findings enables cached inputs. Detector profiles carry
`analysis` labels; use `go tool pprof -tagfocus='analysis=failure'` to focus them.

Local artifacts are retained in the gitignored `.context/performance/` directory:
`baseline.log`, `baseline.{cpu,mem}.pprof`, baseline CPU/allocation summaries;
`query-plans-before.json`, `query-plans-after.json`, `workload-sizes.json`;
`v2.log`, `v2.{cpu,mem}.pprof`, v2 summaries; and
`final-verified.log`, `final-verified.{cpu,mem}.pprof` for the final original-input
run and its CPU/allocation summaries; `legacy-final-comparison.json` and
`legacy-final-tool-comparison.json` for the original implementation audit.
Earlier experiment logs/profiles remain available but are not the final result.
Validation receipts are `go-suite-final.log`, `race-final.log`,
`legacy-keys-scoped.log`, `recovery-scoped.log`, and `ui-rerun.log`.
These local replicas and profiles are not checked into Git.
