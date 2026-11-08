# Execution verification on the production copy

This records the November 7, 2026 verification follow-up to the execution stack.
It is a working evidence record, not production acceptance. The scheduled engine
has not been enabled on the live library by this work.

## Corpus and isolation

A completed product online snapshot was made on Euclid, using the existing
SQLite snapshot/WAL-growth guard, shared capture locks and library-use lease.
The source backup history was checked byte for byte before and after. Both the
copy and every trial write stay on Euclid, outside the live library. This is an
experiment copy on the same drive, not a disaster-recovery backup.

The snapshot contains **7,910,748 messages, 48,581 conversations and 55,214
workspaces**, at schema 24. Initially it had 645,058 labeled messages, built
label state for all 48,581 conversations, and no pending writing-estimate,
label, label-query-fact or timing rows in the inspected queues.

The retained baseline is commit
`108c0367bab0b94466fb0f1c8114029e8d4d4a3d`. Its service normalized a fresh clone;
SQLite `quick_check` returned `ok` in 1,720 seconds. Preparation, normalization
and integrity checks are outside measured execution. Model weights, worker
executables and native runtimes are copied and frozen before creating arms.
Arms have private catalogs, configuration, HOME, temporary files and logs, with
no live source declarations, cloud settings or inherited application tokens.
FP32 is explicitly pinned: configuring a frozen model directory changes the
application's implicit variant default to INT8. The first reference with that
mismatch was interrupted and discarded; it is not comparison evidence.

Normalization under the experiment's UTC timezone invalidated query facts for
all 48,581 conversations and left one label conversation dirty. The initial
representative master therefore includes that full query-fact catch-up, as well
as the explicit 20-conversation invalidation below. It must not be described as
only 20 conversations of total work. A fully settled retained-source preparation
is needed for a separate small incremental workload.

Two derived workloads preserve canonical records and existing writing
classifications:

- **Representative rebuild:** remove labels from the 20 most recently active
  conversations that actually have label outputs; dirty their timing and label
  query facts. This removes 2,303 label rows. Whole-history demand and an
  explicit recent request use the same corpus in both engines.
- **Full label rebuild:** remove all 645,058 label rows and built-conversation
  rows; dirty labels, timing and query facts for all 48,581 conversations.
  This is the expensive label pipeline, not every analysis in the application.

Raw records, selected identities, provider files, descriptors and detailed
reports remain private in the experiment directory and gitignored `.context/`.
Only aggregate evidence belongs in this document.

## What the probes establish

`TestWorkReplica` uses identical measurement source compiled against the
retained baseline and candidate. It measures the real product label query,
full queue settlement, CPU, simultaneous process-tree footprint, I/O lower
bounds and model processes. It also samples Go heap and native-child residency
through an idle period and forced GC, reporting missing attribution explicitly.

Deterministic provider-file replay declares whether conversations are new or
extensions. Every wave must increase both its explicit count query and a census
of messages in those exact identities; undeclared conversation counts must stay
unchanged. A replay failure cancels work immediately. Optional Git enrichment
is disabled symmetrically for replay, since original repository directories are
not frozen inputs.

Arrival overlap requires every wave to become pending while primary history is
still unfinished in **both** arms. Follow-up drains cannot establish overlap.
For scheduled requests, the last observation of a live request is the end bound;
the first observation of completion is insufficient. This establishes overlap
with unfinished history, not necessarily with an indivisible relational pass.

`TestWorkTrial` drives the real work API over real domain tasks and models:
sharing, cancellation, Stop/Resume, allowance changes, graceful release/reopen,
recent recovery and bounded resource inspection. An idle task, an allocation
already below the new cap, an empty recent population or a hold never observed
is **untested**, not a passed proof. Cancellation continuation must concern the
same kind/batch, and per-kind failure counts must not grow.

## Evidence so far

| Verification | Result |
| --- | --- |
| Completed fresh snapshot and isolated normalization | Passed |
| SQLite integrity check on normalized copy | Passed |
| Retained binary recovers interrupted dependency work | Passed, deterministic model fixture |
| Retained binary recovers scheduled publication, then candidate handles another edit | Passed, deterministic model fixture |
| Probe fixtures and independent evidence-integrity reviews | Passed; review findings corrected |
| Harness unit tests | 36 passed |
| Go fast tier and full Go suite | Passed |
| Web checks and full JavaScript matrix | Passed |
| Real-model lifecycle trial on the full invalidation copy | 15 checks passed; sharing untested; INT8 model |
| Focused real-domain sharing/cancellation on a fresh copy | Six checks passed; shared timing batch observed |
| Representative classic A/A | Semantic parity; calibration refused for host noise and >20% spread |
| First representative classic/scheduled pair | Semantic parity; performance rejected |
| Corrected representative classic/scheduled pair | Semantic parity; CPU and recent-readiness gates still rejected |
| Reverse-order representative pair | Stopped after rejection |
| Full label-pipeline paired settlement | Deferred at the representative performance gate |
| Actual arrivals prioritized during unfinished history | Deferred at the representative performance gate |
| Arrival readiness behind an already-running indivisible relational pass | Pending |
| Abrupt process death followed by real-catalog recovery | Passed: real FP32 publication, kill, full integrity check and retained-binary partial resume |
| Sustained resource retention | Two- and eleven-minute samples complete; residual retention attribution unresolved |
| Global projection readiness | Missing publication found and fixed in ordinary scheduling; production-copy rerun remains outstanding |
| Realistic throughput acceptance | Rejected on representative workload; remaining workloads gated |

The lifecycle trial completed in 727.58 seconds. Its concrete observations:

- A stopped labels batch drained in 0.25 seconds. Its 47,908 pending
  conversations stayed pending for the 10-second hold, and Resume used a new
  execution with no failure-count growth.
- Reducing the allowance from 28 to one slot began with three slots allocated;
  the next reading was within the cap. No new over-budget admission was seen,
  and restoring 28 updated both the authority and the private configuration.
- Graceful release took 0.077 seconds against a seven-second limit. Pending
  label, query-fact and timing counts were identical after close and reopen.
  A held task was held before release and unheld after reopening.
- The post-restart recent request sealed all 673 members in about 8.8 seconds,
  while 47,907 label conversations remained pending. Cancelling its remaining
  historical request drained without failure growth or further queue changes.
- Across 142 inspection samples, the maximum resource ledger had 82 tasks and
  the maximum inspection payload was 15,078 bytes, within the declared bounds.

Sharing cancellation was not exercised: the recent request finished before a
shared batch was observed. The authorship hold had no pending domain work and
was also recorded as untested. Abrupt death, real authorship/refinement work and
an already-running relational pass remain separate evidence gaps.

This first lifecycle trial used real INT8 inference, not production's FP32
variant. Its control evidence remains useful; it is not a production-model
performance result. Its sampled peak simultaneous footprint was 2.71 GiB and
whole-process CPU was 4,054 seconds. Those figures are diagnostic: preparation
initially ran concurrently, and measured host activity exceeded the comparison
noise threshold. Measured comparison arms run sequentially after that trial;
noisy pairs keep the existing refusal rules.

## Initial FP32 reference

The retained-source reference completed the full demanded queues in 295.83
seconds of execution and 409.04 CPU seconds. The query-fact stage occupied
273.69 seconds; the label stage occupied 18.11 seconds. These stage CPU/window
measurements include concurrent product polling, not isolated task CPU.

The reference's final product query returned 200 rows in about 55–57 ms.
The first measured classic arm reached the pinned recent product milestone in
26.30 seconds and full settlement in 335.09 seconds. A second classic arm
settled in 264.28 seconds, using 377.95 CPU seconds versus 467.47 in the first.
The A/A check refuses calibration because of other host activity. Its symmetric
spread check also exceeds 20%; a large speedup counts as instability too.

The reference's sampled peak simultaneous footprint was 3.21 GiB. After the
120-second idle period and forced GC, native children were gone and process-tree
footprint retained about 235 MiB above work start. Resource retention is a
diagnostic here, not evidence of a leak or an enforced preparation-memory grant.
Host activity exceeded the quiet-comparison threshold and remains a limitation
that the paired reports must preserve.

## First FP32 comparison and profile

The first scheduled arm matched every declared semantic digest and the pinned
product result, but failed the performance boundary against the second classic
arm. These observations are contaminated by host load and are not precise
quiet-machine ratios:

| Metric | Classic | Scheduled | Ratio |
| --- | ---: | ---: | ---: |
| Declared queue settlement | 264.28 s | 303.39 s | 1.148 |
| Process-tree CPU | 377.95 s | 874.79 s | **2.315** |
| Read bytes | 36.19 GB | 44.19 GB | **1.221** |
| Write bytes | 1.47 GB | 1.06 GB | 0.725 |
| Peak simultaneous footprint | 3.47 GB | 3.62 GB | 1.045 |
| Recent product usable | 22.89 s | 30.47 s | **1.331** |

Remaining repetitions were stopped after that rejection. A separate diagnostic
Go CPU profile confirms material overhead in canonical revision reads (112
sampled CPU seconds), repeated workspace representative reads (81 seconds),
and product-readiness polling (234 seconds). Those are cumulative call costs,
not additive exclusive task measurements; native inference is outside the Go
profile. Compilation/tests overlapped part of that diagnostic, so it supplies
call-stack evidence, not another throughput result.

Two small corrections preserve the existing publication and revision checks:
only completed/superseded units need the second revision read, and the scheduled
query-fact adapter now uses the classic projector's adaptive message budget.
A proposed 16-pending-conversation cap was deferred: the observed facts batch
covered about 31 conversations on average, so that cap could increase commit
count.

## Corrected FP32 comparison

Fresh arms built from candidate commit `23176d96` and the retained baseline
used identical common measurement source and the same frozen workload. Every
declared semantic digest and the pinned product result matched again:

| Metric | Classic | Scheduled | Ratio |
| --- | ---: | ---: | ---: |
| Declared queue settlement | 260.02 s | 260.17 s | 1.001 |
| Process-tree CPU | 370.44 s | 693.21 s | **1.871** |
| Read bytes | 37.58 GB | 43.04 GB | 1.145 |
| Write bytes | 1.57 GB | 0.96 GB | 0.612 |
| Peak simultaneous footprint | 3.45 GB | 3.41 GB | 0.987 |
| Recent product usable | 23.69 s | 29.53 s | **1.247** |

Each arm showed one native model-worker start at the one-second sampling
resolution; no repeated model loading was observed. The adaptive facts budget
reduced scheduled facts batches from 1,552 to 646.
The corrected run improves on the first candidate, but still rejects on CPU,
first correctness and usable recent-query latency. Other host activity averaged
about 3.2–3.3 cores during work, so the exact ratios remain contaminated. The
classic CPU and settlement measurements are close to the preceding control;
there is no basis for waiving the rejection boundary.

After the 120-second idle period and forced GC, neither arm retained a native
model child. The classic retained about 243 MiB above work start; the candidate
retained about 467 MiB, including about 90 MiB additional Go heap in use. These
are diagnostic process-retention observations, not exclusive task attribution
or proof of a leak. The candidate's lower sampled peak does not resolve that
retention difference.

A read-only ownership review found no unbounded retention. In particular, the
scheduled model task deliberately keeps its library-sized preparation context
for ten minutes after last use, independently of native worker expiry. The
runner checks that expiry on its reconciliation tick. Finite request membership
is released when the request ends; the authority's zero reserved request bytes
does not include model preparation or SQLite's connection page caches.
A separate eleven-minute idle diagnostic completed with about 421 MiB of
process footprint retained above work start, including about 40 MiB of Go heap
in use. It retained no native model children. This is lower than the shorter
candidate observation, but the remaining difference from classic still needs
attribution. It overlapped the recovery integrity scan and supplies no
throughput evidence. Source ownership explains a possible cause, not a measured
allocation of the retained bytes to that context or proof of its exact expiry.

Reverse-order repetitions and hours-long full label/replay workloads were
stopped/deferred at this gate. Safety and recovery trials continue separately;
passing those cannot make the performance result acceptable.

## Global projection completion defect

Inspection of the completed catalogs found a completion state that the declared
pending-row checks did not cover. Classic published the current
`message_label_facts_ready` value; the scheduled finite-only run left it absent.
The final product-query sample was about 53 ms for classic and 1,164 ms for
scheduled. Query rows matched, but scheduled kept using the live fallback.
The settlement numbers above therefore describe the declared queues, not proof
of equivalent global projection completion.

The missing publication also affected ordinary production scheduling: the
scheduled adapter never called the native whole-queue finalizer, and the
coordinator stopped requesting work once the dirty table became empty.
Commit `185f8705` repairs this with the existing library cleanup phase and
domain-native generation/publication guard. The coordinator keeps demand while
the queue is empty but readiness remains unpublished. Finite selections still
make no global readiness claim. A dirty row arriving before cleanup is handled
by the native refresh before any readiness publication.

Real-adapter tests cover selected partial and whole drains, ordinary library
completion, no resubmission once ready, and a newly dirty row before cleanup.
Independent review found no correctness issue. The Go fast/full suites, web
checks and full JavaScript matrix passed after this fix.

The measured FP32 binaries predate this ordinary-library fix. Their finite-only
path is unchanged, and no new throughput acceptance is claimed. Future
whole-library benchmarks must drive the ordinary completion path and check the
native readiness marker as well as empty queues and matching rows.

## Focused sharing trial

A fresh full-invalidation copy exercised actual sharing rather than inferring
it from request success. A running stored-timing batch of 128 conversations
listed both historical and recent requests as demanders. Cancelling the recent
request ended that request; the same shared batch then committed all 128
conversations, and historical demand continued without failure-count growth.

Cancelling the remaining requester drained work. Pending counts stayed fixed
over the hold, with 48,325 timing conversations and all 48,581 label
conversations still pending. All six checks passed in 83.81 seconds; inspection
was bounded to 12,182 bytes and the ledger to 13 tasks in this trial.

The configuration pinned FP32, but this sharing proof used real timing work;
it does not establish native-model sharing or rerun the earlier Stop/Resume,
CPU-change and graceful-restart checks. Crash recovery is a separate trial.

## Actual crash and retained-binary resume

On another fresh full-invalidation copy, the scheduled FP32 worker committed
259 labels while 48,577 conversations remained label-pending. The harness then
sent SIGKILL to its verified isolated process group, observed exit status -9
and confirmed the group had drained. The committed label count and pending
count were identical after the kill.

The harness recovered/checkpointed that copy and ran a full SQLite
`quick_check`: `ok`, in 1,316.46 seconds. It then opened the same copy with the
actual retained service and frozen FP32 model. The retained process exited
successfully in 21.23 seconds and published one further label, with zero
reported message or conversation failures. Labels increased from 259 to 260;
canonical counts stayed at 7,910,748 messages and 48,581 conversations.

The retained command deliberately used `--limit 1` and reported a partial run;
this is resumability evidence, not full historical settlement. No snapshot
restore or canonical-record reimport occurred between the candidate and
retained writer. The earlier deterministic dependency/publication recovery
fixtures cover the additional affected-dependent and subsequent-edit cases.

## Remaining release gates

Production stays on classic. The representative CPU and recent-readiness
regressions remain blockers. Remaining evidence includes reverse-order/quiet
calibration, full label and full derived settlement, the ordinary projection
completion path on the production copy, actual source-arrival priority,
responsiveness behind an already-running relational pass, and attribution of
residual retained memory. Real authorship/refinement, disable/re-enable,
global pause and Retry were not exercised by these production-copy trials.

## Decision rule

Passing fixtures and review establish useful safety properties, not realistic
throughput. Production acceptance still needs semantic parity, actual recent
product-query readiness, representative and full settlement, rollback/recovery
and bounded resource evidence. The existing 20% rejection boundary applies to
wall time, CPU, memory and I/O where the metric exceeds its noise floor. An
unavailable measurement or contaminated comparison stays unresolved.

All production activation and comparison-path retirement decisions remain gated
by those results. See [the acceptance protocol](work-execution-acceptance.md).

## Portability integration after the trials

The stack now includes the November 7–8 platform adapters through master
`e0d34885`. [The portability contract](work-portability.md) separates portable
scheduling and cooperative CPU grants from native observations and actions.
Task accounting and resource history preserve unavailable counters, verified
process identity and memory lower bounds. The two manual probes share one
physical-memory sampler: missing physical counters are null, while Go runtime
memory is recorded separately. These changes do not extend native OS support.

The production-copy measurements above remain evidence for their recorded
source commits. No performance trial was rerun for this integration, and no
performance acceptance or production activation is implied.
