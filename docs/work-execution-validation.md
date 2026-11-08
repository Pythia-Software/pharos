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
| Harness unit tests | 34 passed |
| Go fast tier and full Go suite | Passed |
| Web checks and full JavaScript matrix | Passed |
| Real-model lifecycle trial on the full invalidation copy | Running |
| Representative classic A/A and classic/scheduled pairs in both orders | Queued after lifecycle trial |
| Full label-pipeline paired settlement | Pending |
| Actual arrivals prioritized during unfinished history | Pending |
| Arrival readiness behind an already-running indivisible relational pass | Pending |
| Abrupt process death followed by real-catalog recovery | Pending; graceful restart is a separate check |
| Sustained resource retention and realistic throughput acceptance | Pending |

The lifecycle trial's resource figures are diagnostic: preparation initially
ran concurrently. Measured comparison arms run sequentially after that trial.
Host activity is recorded; noisy pairs keep the existing refusal rules.

## Decision rule

Passing fixtures and review establish useful safety properties, not realistic
throughput. Production acceptance still needs semantic parity, actual recent
product-query readiness, representative and full settlement, rollback/recovery
and bounded resource evidence. The existing 20% rejection boundary applies to
wall time, CPU, memory and I/O where the metric exceeds its noise floor. An
unavailable measurement or contaminated comparison stays unresolved.

All production activation and comparison-path retirement decisions remain gated
by those results. See [the acceptance protocol](work-execution-acceptance.md).
