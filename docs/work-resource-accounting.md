# Resource history and task accounting

This is the contract for the resource history above the panel's Load row and
for per-task resource records. It implements
[work-execution-plan.md](work-execution-plan.md) §7.4 and §10.1–10.2. The
code is in `internal/archive/resources_history.go` (history) and
`internal/archive/work_resources.go` (tasks). Both extend the process sampler
in `resources.go` described in [background-work.md](background-work.md) §10.

Both are dormant under classic execution. The history collector runs only
when its owner starts it, which happens with the scheduled engine. Until then
Pharos samples only while someone is looking, as §10 of background-work.md
says, and the history routes answer `available: false`.

## One collector per process tree

| Process | Collector | Scope label |
| --- | --- | --- |
| The app's broker | One, over the broker, the verified native app, every library service and lease, and their descendants. Each library is a subset scope. | `Pharos app (covered processes)`, or the library's name |
| A standalone `pharos serve` | One, over itself, its library's leases and their descendants | `this library service` |
| A service whose broker collects | None. The broker measures it | — |
| CLI commands | None. Their owner measures them through their lease | — |

The collector reads through the process's existing `resourceSampler` and the
shared GPU sampler, so no process is read by two samplers. While it runs,
status snapshots reuse its latest summary (`resourceHistory.latest`) instead
of sampling again, as long as that summary read every process of status's own
lease probe (the one-second `leaseWatch` policy). A summary missing a live
lease, such as a command just started in Terminal, is not used: status samples
the processes itself and marks the collector's lease cache stale, so its next
tick probes the lease files again.

The broker builds its process list from library children and lease files.
Lease files are probed at most every 30 seconds while work runs and every
30 minutes while idle, so the collector does not keep library drives awake.
Processes already discovered are still sampled each tick, and their OS
start identity is checked so PID reuse cannot inherit cached ownership. A newly started
external CLI can therefore be absent from the history until the next lease
probe; a standalone service's status probe shortens that (above), the
broker's does not. It
reads the OS process table once per tick, and descendants belong to their
nearest known root, so a process is counted once. It never asks a service
for readings: a service marked as broker-owned samples nothing itself. That
mark is passed only when the broker actually collects; otherwise the
service owns its readings. Classic app startup does not inspect every
library configuration to discover an engine choice.

For task markers only, the broker asks each running library service
`GET /api/resources/active` every tick. The service answers from memory: its
registry's top-level operations that have not ended, with kind, priority,
trigger, state, libraries and start times. It never runs a catalog query or
`/status/freshness` for it, and the broker reads at most 128 KiB of the
answer. The lease files are probed first; then the libraries are asked at
once under one 100 ms deadline for the whole tick, so N libraries delay the
process readings by at most 100 ms, not N × 100 ms, and a slow library
cannot use up another's time. A library that has not answered by then, or
that answered with an error, is unknown for that tick: its scope lists "Task
markers could not be read for this library", and its known tasks carry
forward, neither ended nor restarted, for at most one minute since it last
listed them. After that they end (observed), so a wedged service neither
keeps stale running tasks nor holds the active cadence. With the scheduled
engine this costs N local requests on every tick, every 2 to 10 s, even
with the panel closed.

The native app is included only when its ownership is verified, by the
broker's parent PID plus its process start time. Until then the scope lists
it as not measured. Ownership never comes from matching a process name.

## Cadence and samples

- **Cadence.** Every 2 s while an operation is running or the scope uses at
  least 10 % of one core. Every 10 s otherwise. An operation boundary can
  wake the collector early (`poke`).
- **Stale gap.** While collecting, the sampler's stale gap is 25 s (2.5× the
  idle cadence), so idle readings still give a rate. The GPU continuity check
  is widened the same way. Without a collector the gap stays at 10 s.
- **GPU.** GPU keeps its own 5 s sampler. GPU samples are stored with their
  own timestamps, never aligned to CPU sample times. The GPU series is
  available, and `gpu` is non-null, only once a GPU value was actually read
  for the scope. A reader that only ever fails (an Intel Mac, a VM) gives no
  GPU row, not a row of nulls.
- **Window.** 15 minutes, bounded by time. A slice of samples is kept per
  scope.
- **Values.** Values are stored exactly. The document rounds CPU and GPU to
  0.1 % and bytes to whole bytes. The status document's rounding is
  separate.
- **No repeated times.** A sample is stored only when its reading is newer
  than the last one.
- **Missing readings.** A tick with no readable process is a sample with
  null values at the tick's time. A null is never drawn as zero.
- **Wall time.** Sample, segment, marker and window times are wall-clock
  times with Go's monotonic reading stripped. The monotonic clock pauses
  while the Mac sleeps, so it would hide the gap and keep pre-sleep samples
  in the window. The sampler's own rates still use monotonic time.
- **Segments.** A segment starts when the collector starts, after a tick gap
  longer than 25 s (the Mac slept), and when a scope's service or broker is
  replaced: a new PID, or the same PID with a new start time. The first rate
  after a boundary is null, because the sampler starts a new baseline.
- **Coverage.** A coverage event is written when the set of measured
  processes changes, or when the list of what was not measured changes. An
  unreadable live process is named. An exited process is not an omission.
  Off macOS the scope says only this process can be read.
- **Markers.** Start and end markers for top-level operations come from the
  tasks reported at each tick. Queued and blocked work is not load: a task
  gets a start marker only once it is seen running. The start is the time it
  began running when that is known (an operation registered running);
  otherwise, for work that waited in a queue, it is the first tick that saw
  it running, marked `observed: true`. Once started, a task that becomes
  blocked keeps its start. An end is the first tick that no longer reported
  the task (`observed: true`). `mark` lets an owner record an exact end in
  its place; where no owner calls it, ends are observed. A task from a
  library whose list could not be read is carried forward, not ended. At most
  400 markers are kept, and markers older than the window are dropped, so a
  task running longer than 15 minutes has no start marker in the window and
  is not listed as running at a time the strip inspects.

## History document

`GET /api/resources/history[?since=]` (service).
`GET /api/app/resources/history[?library=ID][&since=]` (app).

`since` is Unix milliseconds or RFC 3339. With `since`, the document carries
the samples, segments and coverage after that time, compared at millisecond
resolution, plus the coverage in effect at that time; passing back the last
`t` received never returns that entry again. Markers are always the bounded
complete set (`markers_complete`).

```json
{
  "version": 1, "available": true, "scope": "this library service", "library": "",
  "instance": "1234-…", "generated_at": "…", "now": 1762516800000, "since": null,
  "window_seconds": 900, "active": true,
  "cadence": {"active_seconds": 2, "idle_seconds": 10, "gpu_seconds": 5},
  "series": {
    "cpu":    {"unit": "percent_one_core", "basis": "proc_pid_rusage", "available": true, "max": 1000},
    "memory": {"unit": "bytes", "basis": "proc_pid_rusage", "available": true},
    "disk":   {"unit": "bytes_per_second", "basis": "proc_pid_rusage", "available": true},
    "gpu":    {"unit": "percent_driver_time", "basis": "IOAccelerator.AppUsage.accumulatedGPUTime", "available": true}
  },
  "samples": {"t": [], "cpu": [], "memory": [], "disk": [], "cpu_partial": [], "disk_partial": [], "processes": []},
  "gpu": {"t": [], "value": [], "partial": []},
  "segments": [{"t": 0, "reason": "started|gap|restart", "detail": ""}],
  "coverage": [{"t": 0, "covered": [{"pid": 1, "role": "service"}], "omitted": []}],
  "markers": [{"t": 0, "event": "start|end", "id": "", "kind": "", "observed": false}],
  "scopes": [{"library": "ID", "label": "Name"}]
}
```

All `samples` arrays have one entry per time in `t`. `gpu` is null on a
platform without a GPU reader.

`partial` marks a lower bound, from a process or GPU client still waiting
for its second reading. Without a collector the document is
`{"version": 1, "available": false, "reason": "…"}`. When the collector runs
but has not yet read the requested scope, it adds `"pending": true`.

The store fetches the whole window when the panel opens. While the panel is
open, it fetches with `since=` whenever a snapshot carries a newer
`resources.sampled_at`, at most once every 2 s and one request at a time. An
unavailable document without `pending`, or a failed response (an older
service has no route), stops history requests until the panel is opened
again, so classic execution stays dormant. History never goes into the
status stream.

## The strip

There are four rows on one time axis. Each row has its own scale and unit:

- CPU, as % of one core. The scale starts at one core.
- GPU, as the driver's GPU-time share, which is not device saturation.
- RAM, as physical footprint.
- I/O, in bytes per second. The row is left out where it is not measured.

Lines break at nulls, at segment boundaries, and at sample intervals over
25 s. A lone sample is drawn as a short dash.

Each row has an accessible text summary: the value now, the window's peak,
any gaps, and any lower bounds. Nothing animates.

Task markers are drawn across every row and again on their own Tasks lane
below the rows, ▸ for a start and ◂ for an end, each with its words as a
tooltip.

Hovering, or using the arrow keys on the focused strip, shows the nearest
values, the coverage and the task kinds running at that time. That a task was
running then is overlap, not attribution. A click, or Enter or Space on the
focused strip, pins the inspected time, so moving the pointer no longer
changes it. Another click unpins (inspecting the clicked time), and Enter or
Space toggles. Home and End go to the first and last samples. Escape clears
the inspection first, before a second Escape closes the panel. A pinned time
that leaves the window is let go. Only keyboard inspection is announced. The
inspected time is transient. It is not URL state.

## Task records

`taskResources` is created per task by its owner (an operation or executor
batch). Domain code reaches it with `taskResourcesFrom(ctx)`, and every
method accepts nil. Worker paths update atomics only. There is no registry
lock, no journal write and no OS call per message.

Every figure is `{value, unit, basis, coverage}`. A null value means unknown,
which is not the same as zero. The bases are:

| Basis | Meaning |
| --- | --- |
| `measured` | Read directly for this task, such as a dedicated helper's counters over its exclusive ownership interval. |
| `counted` | Counted by the task's code: logical bytes, work units, owned buffers. |
| `sampled` | From overlapping samples, with coverage. The process-tree context is sampled. |
| `allocated` | A grant. Four allocated CPU slots is not evidence of four cores used. |
| `shared` | Real usage that other work shares, with no per-task figure. Go process CPU and memory are always shared. |
| `unavailable` | Not measured. |

The record holds:

- **Allocation.** The current grant, slot-seconds and peak slots.
- **Time.** Elapsed and active, where active is wall time with any admitted
  span open. Overlapping spans count once. Only scheduled-work paths open
  spans. A task that never opened one (a classic operation, a backup, a
  parent execution or request) has active time `unavailable`, not 0.
- **Waits.** By kind: queue, dependency, capacity, writer, power and lock.
- **Work units.** Per phase, mirrored from `reportWork`.
- **Logical I/O.** Bytes the task's code read, wrote, and held as temporary
  data.
- **Owned buffers.** Current and peak.
- **Helper intervals.** Process counters read at admission and at release.
  An interval is `measured` only while the task owned the helper
  exclusively. A helper replaced during the interval is `unavailable`.
- **GPU time.** Only from explicit device or helper reports, or exclusive
  samples. Otherwise `unavailable`.

At the end, `finish` adds, as `context.process_tree_cpu_seconds`, the scope's
CPU integrated over the task's interval. That figure is `sampled`, shared with
other work, and carries its sample coverage. It is never divided among tasks.
It is added only where the task's own registry has the collector: a
standalone `pharos serve`, and the broker's own operations. A service the
broker manages has no collector, so its tasks carry no process-tree context;
the service does not sample itself to supply one. For a task longer than the
15-minute window, the coverage falls below 1 and says so.

`compact()` is written once into the operation's `detail_json` when the task
ends. It keeps non-null values with their units and bases, needs no schema
change, and older binaries ignore it.

`taskResourceLedger` keeps the last 20 summaries per kind and 200 in all, in
memory. Its `export(kind)` is the structured export: summaries plus the basis
vocabulary, with no transcript content.

`taskProfileLabels` gives `op_kind`, `op_id`, `root` and `batch` labels for
`pprof.Do` around task work. The existing bounded
`/api/diagnostics/profile` carries them.

The panel's running lines show a compact resource line when an operation
carries `resources`. It uses the same record. A value with no number shows
its basis word ("shared", "not measured").

## Limits

- Exited children's disk I/O is not counted by the kernel.
- GPU driver time is best-effort.
- Peak memory for the tree is the simultaneous reading. It is never the sum
  of per-task peaks.
- On Apple silicon, GPU and model memory are not added to process footprint
  as if they were separate.
- The history is disposable diagnostic state. It is never evidence that work
  is ready.

## Work inspection contract

During the comparison window, `GET /api/work` exposes bounded request/task
facts and CPU allowances; `POST /api/work` uses an explicit `action` plus
that action's arguments. IDs, states, kinds, scopes, modes, blockers,
counts and allocation fields drive the controls. Member identities use
`id` and `revision`. Decision traces and `*_ns` timings are diagnostics;
clients should tolerate additional fields and unknown decision types.
Requests and all inspection state are disposable across process restarts.

Model preparation sessions close when disabled or after ten idle minutes.
Their retained Go context is currently included in shared process memory,
not a measured per-task buffer figure or an enforced context-memory grant.
Quantifying and bounding that residency alongside model memory is still an
activation gate; the CPU allowance does not imply a memory cap.
