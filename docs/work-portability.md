# Work execution across platforms

The execution model is portable policy over domain work. Native observations
and native actions stay at the edges. This is preparation for future ports;
it does not enable a new supported operating system or change the scheduled
engine's production activation gates.

## Follow the existing boundaries

The November 7–8 portability changes establish the pattern this stack uses:

- [#508](https://github.com/Pythia-Software/pharos/pull/508), #515–#525 and
  #537 put advisory locking in `internal/platform/filelock`. An unavailable
  lock is an error, distinct from contention. File lifetime and durable work
  ownership remain with the caller.
- [#535](https://github.com/Pythia-Software/pharos/pull/535),
  [#538](https://github.com/Pythia-Software/pharos/pull/538) and
  [#543](https://github.com/Pythia-Software/pharos/pull/543) provide
  `filesync`, `localidentity` and `fsinfo`. Native facts carry availability;
  shared callers decide whether a safe fallback exists. A display can omit
  capacity; a write requiring known free space must refuse.
- [#540](https://github.com/Pythia-Software/pharos/pull/540) and
  [#548](https://github.com/Pythia-Software/pharos/pull/548) separate resource
  arithmetic from native accounting. Every reading declares its counters;
  missing counters and resets cannot become measured zeroes.
- [#533](https://github.com/Pythia-Software/pharos/pull/533),
  [#541](https://github.com/Pythia-Software/pharos/pull/541) and
  [#552](https://github.com/Pythia-Software/pharos/pull/552) separate process
  isolation, identity and liveness. Unknown liveness is not absence, and
  presence alone proves neither ownership nor identity across PID reuse.

Use small functions and platform-tagged files, with private helpers beside
archive callers unless a primitive is shared across packages. Do not add an
all-purpose OS interface, a platform service object, or a second resource
sampler. The existing native implementation and unsupported implementation
must obey the same narrowly stated contract.

## Three independent responsibilities

| Responsibility | Contract | Platform boundary |
| --- | --- | --- |
| Decide what should run | Requests, frozen populations, task dependencies, priority, cancellation and retry | None. `work_execution*.go` and domain adapters use contexts and domain readiness. |
| Grant parallelism | One `workAuthority` per process; an admitted leaf receives `workAllocation.CPUs`, passes it to its pools/helper threads, and joins workers before returning the grant | None. A slot is cooperative runnable parallelism, not CPU affinity, a kernel quota, or measured usage. Separate service processes retain separate allowances. |
| Observe what ran | Process CPU/memory/I/O and optional GPU samples; counted task work, buffers and logical bytes | Existing `resources_*.go`/`resources_gpu_*.go` readers. Missing telemetry never prevents ordinary execution or proves readiness. |

The authority does not depend on a GPU driver, process table, machine load
average, or successful resource sampling. A future cgroup, Job Object or
machine-wide broker limit would be a separate enforcement contract, introduced
only with its own lifecycle and native tests. Do not silently reinterpret the
existing CPU setting as such a limit.

## Observations must say what is known

`processUsage.Known` is the source of truth for available counters. Own CPU,
reaped-child CPU, memory, resident size and disk bytes are independent. Unix
block counts are not byte counts. Only counters present at both ends can
produce deltas; a backwards counter invalidates that interval. A real zero
with a present counter remains zero. Go runtime memory is an approximation
of runtime-managed memory, not process RSS or physical footprint.

History preserves nulls, partial totals and the reader's basis. Its memory
annotation describes the actual reader, and missing processes remain visible
in coverage. CPU/GPU/memory/I/O series can be independently unavailable.
Task resource quantities retain the separate vocabulary `measured`, `counted`,
`sampled`, `allocated`, `shared` and `unavailable`; source metadata explains
which native observation supports a measured helper interval.

A helper's per-task CPU needs exclusive ownership, matching nonzero start
identities and usable counters at admission and release. Unknown identity
cannot establish that two readings describe the same process. CPU and memory
availability are independent, and an endpoint memory reading is not a peak or
an additive share of the service's memory. Go service usage stays shared.

Current readers remain deliberately asymmetric: macOS can sample owned
process trees and physical footprint; Linux reads only its own process CPU
and Go runtime memory, with no process-tree reader or GPU reader; other
targets have Go runtime memory only. New ports add validated readers behind
these contracts, rather than returning placeholder native measurements.

Machine load is optional context for timing estimates. Only the Darwin reader
uses `sysctl`, and only the Linux reader reads `/proc/loadavg`; other targets
return unavailable. It is not a resource-admission signal.

## Actions must preserve their guarantees

Cancellation and shutdown must keep a CPU grant until the task's workers
have drained. A native action that cannot provide the required process-group
isolation, advisory lock, identity or durability guarantee must fail through
the existing operation error path. Do not report success after a weaker
substitute. Reuse the existing process, filelock, filesync and identity seams;
do not embed Unix flags or macOS commands in a domain adapter or executor.

Optional observation failure degrades diagnostics; inability to perform a
required action refuses that action. Neither failure erases domain dirty state.
Scheduler history and request inventories remain disposable. The old binary
continues to recover from domain markers without reading scheduler state.

## Proving a future port

1. Extend the relevant native leaf, with explicit units, source, identity and
   failure semantics. Preserve existing persisted identities and API units.
2. Run deterministic shared tests with scripted unavailable, partial, reset,
   replacement and valid-zero readings. Test native actions and refusal before
   side effects in platform-tagged tests; leave native ABI types out of shared
   tests. Cross-compilation checks build boundaries, not native correctness.
3. Run native lifecycle tests on the target OS before claiming support:
   locks/durability, cancellation/drain, child cleanup, process identity, and
   actual previous-binary recovery where a compatible baseline exists.
4. Calibrate and run the acceptance workloads on that OS with matching metric
   bases. Missing metrics block a throughput verdict; macOS trial results
   cannot establish Windows or Linux performance or lifecycle correctness.

The Euclid production-copy harness is presently a macOS/POSIX experiment.
Its process-group supervision and `wait4` CPU basis, macOS footprint and GPU
readers are explicit constraints. Portable fixture tests can still run without
claiming those experiments succeeded on another host. Keep frozen baseline
artifacts and recorded measurements pinned to their original source commits;
integration with newer platform adapters does not retroactively validate a
new candidate binary.
