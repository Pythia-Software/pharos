# Background work: the operation contract

Status: contract for the background-work series (phases 1–4). Phase 0 wrote
it; nothing below exists in code yet except where a section says "today".
When the code and this document disagree, fix one of them in the same
change.

Related: [Progress and time estimates](progress-estimates.md) (the forecast
model this reuses), [Configuration](configuration.md) (today's
`/api/library/status`), [Technical overview](technical-overview.md).

## 1. Scope

**In scope: structural background work that writes.** Long-running or
unattended work that writes to the catalog, to captures, to the library's
drive, to the cloud, or to another library. That is everything Pharos starts
with `Server.spawn`, `Catalog.runBackground`, a CLI write command, or a
broker library operation, plus process lifecycle work that decides whether
the drive can be ejected.

**Out of scope:** reads; page-initiated queries and their caches; Ask
Pharos; card analyses and covers; MCP tool calls that only read; browser
work. These must stay fast and must not appear in, or change, any answer
below.

**Exception: a read that starts a write.** A GET that starts a rebuild
(`/api/authorship`, the usage summary behind the native menu's 60 s poll,
`GET /api/findings` before findings exist, a Query Table request that
rebuilds the Tools rollup) registers that write as an operation with
trigger `read`. Phase 1 registers it; whether a read may keep starting it
is a later decision.

**This Mac only.** Every surface shows only operations whose `host_id` is
this Mac's (`currentHost().ID`). Rows another Mac wrote to a shared
library's journal are never shown, reconciled or pruned by this Mac.

## 2. Operation model

### 2.1 Spec

Every operation is started from a spec. The spec is fixed for the
operation's life.

```go
type OperationSpec struct {
	Kind     string   // a row of the kinds table (§3), e.g. "index.changes.manual"
	Variant  string   // optional sub-kind the kind row allows, e.g. "sample" | "full"
	Subject  string   // optional: source name, destination ID, a new library's name, a volume
	Parent   OpID     // zero for a top-level operation (begin also takes the operation its context carries)
	Trigger  string   // schedule | user | chained | startup | version | read | device | cli | respawn | onboarding | resume
	Holds    []Hold   // what it holds while running (§4); defaults from the kind row
	Affects  []string // freshness domains it brings current (§5); defaults from the kind row
	Priority string   // foreground | background; defaults from the kind row (a child: its parent's)
	Legacy   string   // ID of the record it shadows (SyncRun.ID, BackupRun.ID, cloud run ID…)
	Queued   bool     // register as queued: it waits for its turn
	Libraries []string // broker operations: the registered library IDs they touch
}
```

The journal policy is not in the spec: it is the kind row's, always
(`journalPolicyFor`, with the row's "always for `user`" exception).

```go

type Hold struct {
	Resource string // see §4.1, e.g. "drive", "catalog-writer", "capture:<host>"
	Mode     string // "read" | "write"
}
```

`Kind` must exist in the kinds table (`operation_kinds.go`, one Go file). An
unknown kind panics in tests and is recorded as kind `unknown` in
production; `unknown` makes the eject verdict `unsafe` (§4).

### 2.2 Lifecycle

One state set for every operation:

| State | Meaning | Terminal |
|---|---|---|
| `queued` | registered, waiting for its turn (e.g. behind another run) | no |
| `running` | doing work | no |
| `blocked(reason)` | cannot progress until something else ends; `reason` is a code from §8.3 | no |
| `stopping` | a stop was asked for; it is heading for its next safe point | no |
| `succeeded` | finished its work | yes |
| `failed` | ended with an error | yes |
| `cancelled` | a user stop ended it at a safe point; what it committed is kept | yes |
| `interrupted` | ended early without a user stop: release, eject, quit, crash, or its process died (§6.5) | yes |
| `skipped(reason)` | did nothing, on purpose (`already-running`, `nothing-to-do`, `disabled`, `deferred-to-<kind>`) | yes |

Allowed transitions. Anything else is a programming error: tests fail,
production logs and keeps the old state.

```
queued   → running | skipped | failed | cancelled | interrupted
running  → blocked | stopping | succeeded | failed | skipped | interrupted
blocked  → running | stopping | failed | skipped | interrupted
stopping → cancelled | succeeded | failed | skipped | interrupted
```

`start` and `begin` register an operation as `running`, or as `queued`
when the spec says it waits for a turn (a manual index waiting for the
automatic one to yield). `queued → failed` covers a start that cannot
proceed (a TryLock that fails is `skipped(already-running)`; an error
before any work is `failed`). Work that discovers it has nothing to do
calls `Skip` from `running` or `blocked`.

- A stop that arrives after the last unit of work leaves the run to finish
  as `succeeded` (today's `closeIngestStop` rule).
- `cancelled` is only ever the result of a user stop. A release that stops
  work is `interrupted`: the work did not choose to end, and its next run
  resumes it. This replaces today's mix of "Stopped", "cancelled",
  "interrupted" and `failed` with phase `interrupted` (cloud).
- `deferred` (today's incremental run state when a capture held a source) is
  not a state. Such a run is `succeeded`; each source it left is a child or
  step `skipped(capture-busy)`, and the conversations domain for that source
  reports reason `capture-busy` (§5).
- A child's terminal state never changes its parent's by itself. The parent
  decides: the analysis chain aborts on the first failed stage (today's
  rule), so the chain op fails and the run fails.

### 2.3 Handle API

```go
// start replaces spawn for structural work. It registers the operation
// before work runs, so there is no window in which running work is
// unregistered. It returns false (and registers nothing) when the service
// is stopping, exactly as spawn does.
func (s *Server) start(spec OperationSpec, work func(ctx context.Context, op *Op) error) bool

// Inline registration, for work that runs on the caller's goroutine
// (an HTTP handler, a CLI command, a chain stage).
func (s *Server) begin(ctx context.Context, spec OperationSpec) (context.Context, *Op)

func (op *Op) Run()                            // queued → running; the first Step also implies it
func (op *Op) Plan(steps ...StepPlan)          // declare the steps up front: "step N of M"
func (op *Op) Step(name, unit string, total int64) // start the next step; total < 0 = unknown
func (op *Op) Advance(n int64)                 // add to the step's done count
func (op *Op) SetDone(n int64)                 // set it (counters that are already cumulative)
func (op *Op) Block(reason, detail string)     // running → blocked
func (op *Op) Unblock()                        // blocked → running
func (op *Op) SafePoint(next time.Duration)    // how soon a stop would land; 0 = at one now
func (op *Op) Skip(reason string)              // terminal skipped(reason); work must return
func (op *Op) Child(spec OperationSpec) (context.Context, *Op)
func (op *Op) Wrote()                          // it changed data (for if-longer-than, §6.2)
func (op *Op) Finish(err error)                // terminal state from err and the op's stop mark
```

- `start` calls `Finish` itself with `work`'s result; `begin` callers call
  `Finish` (normally deferred).
- `Finish` maps: `nil` → `succeeded`; an error while the op is marked
  stopped by a user stopper (it went through `stopping` from a stop
  endpoint, see "Stop wiring") → `cancelled`; any other error once the ctx
  is done (release, quit) → `interrupted`; other errors → `failed`. The
  context's cause is not used: today's stoppers (`stopIngest`,
  `cloud.stop`) call plain cancel functions, and phases 1–3 do not change
  them. A panic is `failed`, then re-raised as today (`exitOnFault`).
- Units are real units of the step (`byte`, `conversation`, `workspace`,
  `input`, `repository`, `file`) or empty. "Conversation group" is allowed
  only where it is today, and the snapshot labels it as such.
- `Plan` may be revised once discovery knows more (sources, needed steps);
  revisions only add or drop steps not yet started.
- `SafePoint` defaults from the kind's latency class (§3.1); work that knows
  better (an index inside a huge workspace) reports a measured estimate.
- Handle methods never touch SQLite or take a catalog lock: progress hooks
  can run inside a write transaction (`work_measurement.go`). They update
  memory under the registry's own mutex and wake the journal writer (§6).
- During dual-write the existing models stay the source of their own
  progress, and the registry follows them: a `SyncRun` linked to an
  operation mirrors every `updateRun` into it (its phase and measured work
  become the step; its terminal state, the operation's outcome, which
  `Finish(nil)` applies), and backups, captures, cloud jobs and the Git
  refresh mirror their progress callbacks. Nothing in the registry writes
  to a run, so `libraryActivities` and the old drive panel are unchanged.
  (This replaces phase 0's plan of the handle updating `SyncRun.Work`.)
- Work that returns `nil` once a release cancelled its context did not
  finish: a top-level operation without a recorded outcome then ends
  `interrupted`.

**Stop wiring.** In phases 1–3 the registry observes stops; it does not own
cancellation, so locks and ordering do not change.

- Each kind row names its stopper: the existing function that stops it
  (`stopIngest`, `stopLibraryRefresh`, `backups.stop`, `cloud.stop(id)`), or
  none.
- `POST /api/operations/stop {"id": …}` looks up the operation, calls that
  stopper, and moves the op to `stopping`. The existing stop endpoints call
  the same stoppers, and the registry marks the matching ops `stopping` too.
- Release (`POST /api/release`) cancels `s.life.ctx` as today. Every op whose
  ctx derives from it ends `interrupted` unless it finishes first.
- **Request-scoped work.** An operation started inside an HTTP handler
  (`begin` with `r.Context()`) is not cancelled by a release, and the
  release waits for the request (`s.life.requests`). Such an operation must
  either merge the service's lifetime into its context (preferred:
  `context.AfterFunc(s.life.ctx, cancel)`, as `ask.go` does and the cloud
  connection test does since phase 0), or be declared `release-blocking` in
  its kind row with a truthful `on_eject` ("Eject waits for it, up to N"),
  in which case it makes the verdict `busy` (§4.2). No kind is
  release-blocking today: request contexts derive from `s.life.ctx`
  (`http.Server.BaseContext`), and `index.sources.legacy` runs on
  `beginIngest`'s context, which derives from it too.
- Phase 2 (flagged) may refuse to start drive-holding work once a release
  has begun; until then `start` only records that it started during a
  release (`detail.during_release`). In the service a release *is* the
  stop, and `start` already refuses (registering nothing) once the service
  is stopping, as `spawn` did, so no work starts after a release begins.

### 2.4 Nesting: run → stage → step

Three levels, and only three:

1. **Run**: a top-level operation the user can name ("Capture & index",
   "Index changes"). It has the plan the drive panel counts as "step N of M".
2. **Stage**: a child operation for work that has its own kind, holds or
   history and can also run on its own: each analysis-chain stage
   (`analysis.git`, `analysis.findings`…), a capture inside Capture &
   index, the index inside Capture & index, the transfer inside a delivery.
   A stage is one step of its parent's plan; it may have its own steps.
3. **Step**: a named unit of progress inside a run or stage (a source, a
   discovery pass, "Save the catalog"). Steps have a name, unit, done and
   total, and no lifecycle of their own beyond `skipped` in the plan.

Example, a manual index:

```
index.changes.manual                          run, plan: discover, 3 sources, audit, chain
├─ step "Discover inputs"
├─ step "Codex" (byte), "Claude Code" (byte), "Cursor" skipped(capture-busy)
├─ step "Verify a sample"
└─ analysis.chain                             stage
   ├─ analysis.github  analysis.identities  analysis.git
   └─ analysis.tools   analysis.authorship  analysis.findings
```

The same `analysis.findings` kind started by "Refresh findings now" is a
top-level run with trigger `user`. Grouping is by `Parent`, never by label.

## 3. The kinds table

One row per structural operation in taxonomy families A, B, C, E, F and G,
plus the read-started writes of D2. The Go kinds table holds these columns;
the words here are the words surfaces show (§8).

### 3.1 Column key

- **Process**: `svc` library service (`pharos serve`), `brk` broker
  (`pharos app`), `cli` a Terminal command, `mcp` an MCP stdio server,
  `app` the native app.
- **Holds** (§4.1): `D` the library's drive, `W` the catalog writer, `I` the
  ingest coordinator (`ingestMu` + its flock), `C(h)` host h's capture lock,
  `X(c)` capture c's index lock, `B(d)` a backup destination's lock, `K(d)`
  the cloud job slot and destination row claim, `O` the broker's
  `operationMu`, `Z` `.initialize.lock`, `M` the broker's main mutex.
  `D(source)`/`D(target)` name the drives of a broker operation's two
  libraries. Every hold carries a machine-readable mode, `:w` (write) or
  `:r` (read), e.g. `D:w W:w I:w`; anything in parentheses after the list
  is a note for people, not part of the declaration.
- **Affects**: freshness domain IDs from §5.
- **Stop**: how a user stops it today (`index` = `/api/index/cancel`,
  `view` = `/api/library/refresh/stop`, `backup` = `/api/backup/cancel`,
  `cloud` = `/api/cloud-backup/cancel`), or `—`.
- **On stop or eject**: what a stop or a release does to it. This is the
  `on_eject` text, verified against today's code where noted.
- **Latency**: how soon a stop lands. `instant` < 1 s; `record` between
  records, files or small units, normally < 2 s; `batch` between workspaces
  or batches, which can exceed the 7 s release budget on a large unit; `step`
  only when the current step ends; `none` cannot be stopped.
- **Journal** (§6.2): `always`, `>T` (if-longer-than T, or if it wrote), or
  `never`.
- **Pri**: `fg` foreground (a user is waiting), `bg` background.

### 3.2 A. Ingest: capture and index

| Kind | Label · verb | Trigger | Proc | Holds | Affects | Stop | On stop or eject | Latency | Journal | Pri |
|---|---|---|---|---|---|---|---|---|---|---|
| `index.changes.automatic` | Index changes · Indexing changes | schedule | svc | D:w W:w I:w | conv:\*, view | index | Stops between workspaces; the next index resumes it. | batch | >10 s | bg |
| `index.changes.manual` | Index changes · Indexing changes | user | svc | D:w W:w I:w | conv:\*, view, an.\*, findings | index | Stops between workspaces; the next index resumes it. The chain after the sources ignores Stop and stops only on release. | batch | always | fg |
| `capture.this-mac` | Capture this Mac · Capturing | user, onboarding | svc | D:w I:w C(this):w | capt:this/\* | — (today) | Stops between files; the next capture resumes it, and what was captured stays valid. | record | always | fg |
| `capture.cli` (variant `sqlite-records` for that command's writing subcommands) | Capture (Terminal) · Capturing | cli | cli (lease only; it never opens the catalog) | D:w C(this):w | capt:this/\* | — | Pharos cannot stop it; the drive stays busy until it ends. | none (to Pharos) | never (lease only) | fg |
| `index.captures` | Index captures · Indexing captures | user, onboarding, cli | svc, cli | D:w W:w I:w X(c):w (`I` in the service only) | capt:\*, conv:\*, view, an.\*, findings | index | Stops between records; the next index resumes it. Waits for a running capture (`blocked(capture-running)`). | batch | always | fg |
| `library.update` | Capture & index · Capturing and indexing | user | svc | D:w W:w I:w | capt:this/\*, conv:\*, an.\*, findings, upgrade | index | As `index.captures`; the browser then starts `library.upgrade`. | batch | always | fg |
| `library.rebuild` (variants `full`, `retained`) | Full recapture & reindex / Reindex from stored messages · Rebuilding | user, resume | svc | D:w W:w I:w | conv:\*, view, an.\*, findings | index | Stops between workspaces; resuming the repair continues it (`sync_recovery_jobs`). | batch | always | fg |
| `index.verify` (variants `sample`, `all`) | Verify retained inputs · Verifying | user; `sample` is a step of index runs | svc | D:w W:w I:w (reads retained inputs; writes `sync_verifications` and integrity issues) | — | index | Stops between inputs. | record | always (`all`); step only (`sample`) | fg |
| `index.sources.legacy` | Index sources (legacy API) · Indexing | user (API) | svc | D:w W:w I:w | conv:\* | — | Runs inside the request, with the service's lifetime merged into its context: a release stops it between workspaces. | batch | always | fg |
| `ingest.cli` (variants `ingest`, `repair-existing`) | Index (Terminal) · Indexing | cli | cli | D:w W:w (no `I` today) | conv:\* | — | Pharos cannot stop it. | none (to Pharos) | always | fg |
| `host.add` | Add this Mac (Terminal) · Adding this Mac | cli | cli | D:w W:w | conv:\* | — | Pharos cannot stop it. | none (to Pharos) | always | fg |
| `capture.history-backfill` (variants by subcommand) | Capture history backfill (Terminal) · Backfilling history | cli | cli (lease only) | D:w C(h):w | capt:h/\* | — | Pharos cannot stop it; its native-snapshot pause blocks ordinary capture of the chosen sources until released. | none (to Pharos) | never (lease only) | fg |

Notes:

- **Acquisition** ("preserving inputs", A11) and the periodic SQLite record
  reconciliation are steps of the index runs above, not kinds.
- **Onboarding** (A13) uses `capture.this-mac` and `index.captures` with
  trigger `onboarding`.
- **The wait for a capture lock** (`waitCaptureLock`) is `blocked` with
  reason `capture-running` and detail "the capture on <Mac>". Phase 0 made
  it report the phase `waiting for the capture on <Mac> to finish` through
  the run's work meter; that phase stays until the run's next progress
  report (the index's next phase, normally within a second) and is its own
  measured stage, so the wait is no longer counted as indexing time. With
  `Block`/`Unblock` the op returns to its current step when the lock is
  acquired.

### 3.3 B. The library-wide analysis chain, and D2

The chain runs after manual index runs, capture index runs, Capture & index,
repairs and `/api/index`, in this order; the first failure aborts the rest.
Scheduled indexing never runs it; it marks `sync_deferred` instead (§5).

| Kind | Label · verb | Trigger | Proc | Holds | Affects | Stop | On stop or eject | Latency | Journal | Pri |
|---|---|---|---|---|---|---|---|---|---|---|
| `analysis.chain` | Update analyses · Updating analyses | chained | svc | D:w W:w | an.\*, findings | — | Stops on release only; it runs again after the next index. | batch | step of its parent | parent's |
| `analysis.github` | Check GitHub for renamed repositories · Checking GitHub | chained | svc (+`gh`) | — (network only) | an.identities | — | Stops on release only. | record | step | parent's |
| `analysis.identities` | Link identities · Linking identities | chained | svc | D:w W:w | an.identities | — | Stops on release only. | batch | step | parent's |
| `analysis.git` | Look up merges in Git · Looking up merges in Git | chained, startup (one-time backfill), after a legacy sync | svc (+`git`) | D:w W:w | an.git | — | Stops; it runs again after the next index. Each `git` call has a 5 s limit (Conductor's git has none). | batch | >5 s when not a stage | bg |
| `analysis.tools` | Update the Tools rollup · Updating the Tools rollup | chained, read (a Query Table request), version | svc | D:w W:w | an.tools | — | Stops; it runs again on the next index or Tools request. | batch | >5 s when not a stage | bg; fg when read-started |
| `analysis.authorship` | Classify your writing · Classifying your writing | chained, read (`/api/authorship`, usage summary, native menu usage poll) | svc | D:w W:w | an.authorship | — | Stops; it runs again after the next index. | batch | >5 s when not a stage | bg |
| `analysis.findings` | Update findings · Updating findings | chained, user ("Refresh findings now"), read (`GET /api/findings` before the first build) | svc | D:w W:w | findings | — | Stops; it runs again after the next index. | batch | >5 s when not a stage; always for `user` | bg; fg for `user` |
| `analysis.refresh` | Update analyses · Updating analyses | chained (after a legacy source sync: Git, then tools, then findings) | svc | D:w W:w | an.git, an.tools, findings | — | Stops; it runs again after the next index. | batch | >5 s | bg |

Notes:

- **Single flight.** Each analysis already runs at most once at a time. A
  second request becomes `skipped(already-running)` instead of today's
  silent success. The chain's poll-then-call window, which can mark a
  stage complete without running it, must surface as `skipped`, not
  `succeeded`.
- **Phase names.** Today's internal phase names ("refreshing github") never
  reach users; the labels above do.

### 3.4 C. Continuous maintenance

The loops started in `Serve` (`maintainLibrary`, `maintainSubstringIndex`,
`maintainTextSearch`, `maintainToolSearch`, `maintainQueryTextIndexes`,
`keepWALSmall`, `automaticLoop`, `cloudScheduleLoop`, and the broker's
`maintain`) are **daemons**, not operations: they are registered by name so
the unregistered-work test can account for them, but they have no state,
hold nothing, and are never shown. Each pass that does real work starts an
operation.

| Kind | Label · verb | Trigger | Proc | Holds | Affects | Stop | On stop or eject | Latency | Journal | Pri |
|---|---|---|---|---|---|---|---|---|---|---|
| `maintenance.library-view` | Index the Search view · Indexing the Search view | chained (dirty workspaces), version | svc | D:w W:w | view | view | Stops; the batch in progress is abandoned, and it carries on when Pharos next opens the library or on Resume. | record | >15 s (today's `libraryCatchUpShown`) | bg |
| `maintenance.previews` | Fill in previews · Filling in previews | daemon pass | svc | D:w W:w | view | — | Stops; it carries on later. | record | never | bg |
| `maintenance.parallelism` | Measure parallel work · Measuring parallel work | daemon pass | svc | D:w W:w | — | — | Stops; it carries on later. | record | never | bg |
| `maintenance.cache-warm` | Warm page caches · Warming page caches | daemon pass | svc | D:r | — | — | Stops. | record | never | bg |
| `maintenance.search.substring` | Build substring search · Building substring search | version, chained | svc | D:w W:w | search.substring | — | Stops between batches; it carries on when Pharos next opens the library. | batch | >30 s | bg |
| `maintenance.search.text` | Build text search · Building text search | version, chained | svc | D:w W:w | search.text | — | Stops between batches; it carries on later. | batch | >30 s | bg |
| `maintenance.search.tools` | Build tool search · Building tool search | version, chained | svc | D:w W:w | search.tools | — | Stops between batches; it carries on later. | batch | >30 s | bg |
| `maintenance.search.query` | Build Query Table text indexes · Building table search | version, chained | svc | D:w W:w | search.query | — | Stops between batches; it carries on later. | batch | >30 s | bg |
| `maintenance.upgrade-count` | Count upgrade steps · Counting upgrade steps | version (once the service serves, when the stored count is not this build's) | svc | D:w W:w | upgrade, an.ledger | — | Stops; it counts again the next time Pharos opens the library. | batch | >5 s | bg |
| `maintenance.checkpoint` (variants `bound`, `after-run`, `close`) | Save the catalog · Saving the catalog | daemon pass (WAL over 64 MiB, every 10 s), chained, release | svc | D:w W:w | — | — | Not interruptible; short. | record | >1 s | bg |
| `maintenance.origins` | Publish conversation origins · Publishing conversation origins | daemon pass (`maintainLibrary`: every pass while no ingest runs, at most once a minute during one); the end of each ingest and the Tools rollup publish them as part of their own operation | svc | D:w W:w | — | — | Stops; it publishes them on its next pass. | batch | never | bg |
| `maintenance.mcp-history` | Store MCP call history · Storing MCP call history | read (an MCP call through the service queues its record; one background writer stores the queue, so no request waits for the writer; a direct MCP call stores its own record when it closes the catalog, after its answer is out) | svc, mcp | D:w W:w | — | — | Stops; closing the catalog stores what is still queued. | record | never | bg |
| `ask.save` | Save an Ask Pharos answer · Saving an Ask Pharos answer | user (a finished Ask turn whose save missed its deadline is retried in the background for up to 5 min) | svc | D:w W:w | — | — | Stops retrying; an answer not yet saved is lost, as when the service stops. | record | never | bg |
| `drive.measure` | Check the drive · Checking the drive | read (`GET /api/health/drive`, 5 min cache) | svc (+`diskutil`) | D:w W:w (one `library_size_history` row) | — | — | Stops. | record | never | bg |

**Daemon passes.** A pass's operation is registered before the pass runs,
tentatively: it counts for the verdict but tells no subscriber, and a pass
that did nothing is forgotten without a trace. The first unit of work
publishes it. A stretch of passes that keep finding work (the Search view
catching up, previews, parallelism, an index build) is one operation, from
the first pass that works to the first that finds nothing. These kinds do
not call `Wrote()`, so they are journaled only past their threshold.

**Very frequent tiny work.** WAL bounding checks every 10 s; almost every
check does nothing. The check is the daemon. Only a check that runs a
checkpoint starts `maintenance.checkpoint`, which is registered (so eject
sees it) but journaled only past 1 s. The same pattern applies to every
`>T` row: register always, journal only past T or when `op.Wrote()` was
called. `drive.measure`, `maintenance.origins`, `maintenance.mcp-history`,
`ask.save` and `cloud.reconcile` (§3.6) are
listed only so the unregistered-work test passes and eject sees them; they
are D-family and hidden from surfaces. Requests never write: the writes
they used to make (origins, MCP history, interrupted cloud runs) are these
background operations, and no request waits for one. Ask Pharos stays out of scope (§1): only the background
save of a finished answer, which writes the catalog after its request has
ended, is registered.

**A pinned WAL.** A bound checkpoint that a reader or the writer keeps
busy ends `skipped(catalog-busy)`. Once the WAL is over `walPinBytes`
(1 GiB) and the truncating pass reports frames left to copy at the same
`nBackfill` as the previous busy check, the checkpoint has stalled, and the
skip diagnoses it (`wal_pin.go`), at most once a minute per stall and again
at once for a new one. It reads the -shm header and asks `F_GETLK` which
other process holds the write, checkpoint or a read lock (macOS), using the
descriptor SQLite already has open, so it opens and keeps nothing on the
library's volume. Only another process's read lock on a slot marked where the
checkpoint stopped (below the last frame) makes it the holder, or its lock
on slot 0 while frames are left: such a reader started when the log was
fully copied (just after a truncation) and reads the database alone, and
no checkpoint copies anything until it ends. A lock is tied to a mark only
if the header (both copies agreeing, checksum valid) reads the same before
and after the lock probe; otherwise the process is named as possible; the writer,
a checkpoint, a recovery or a reader at the last frame are only mentioned.
With no such outside reader it names this process when a slot is marked
where the checkpoint stopped, "probably" until a second probe agrees, with
its running operations that started before the stall, oldest first;
otherwise the holder is unknown. The stall's start is tracked at any size
(only the diagnosis waits for 1 GiB), and a check whose truncating pass
reports no counts leaves it as it was. The diagnosis goes in
the skipped operation's detail, one stderr line and attention rule 9
(§7.3). It cancels nothing.

### 3.5 E. Transitions

| Kind | Label · verb | Trigger | Proc | Holds | Affects | Stop | On stop or eject | Latency | Journal | Pri |
|---|---|---|---|---|---|---|---|---|---|---|
| `catalog.initialize` | Prepare the catalog · Preparing the catalog | every writable catalog open (skipped when `meta.initialized_build` matches this executable) | svc, cli, mcp | D:w W:w Z:w | all derived domains | — | Cannot be stopped; the service has no API until it ends. A kill rolls back the step in progress, and the next open re-runs from the last committed `meta` key. | none | >2 s | fg |
| `library.upgrade` | Upgrade the library · Upgrading the library | chained (after an index finds pending steps, or the browser after Capture & index), user (`POST /api/upgrade`), cli | svc, cli | D:w W:w | upgrade, an.tools, an.ledger | — | Stops after the unit in progress; starting the upgrade again resumes it. | record | always | fg |
| `catalog.repair` (variants `refine-usage`, `repair-usage-attribution`) | Repair usage (Terminal) · Repairing usage | cli | cli | D:w W:w | an.tools, an.authorship | — | Pharos cannot stop it. | none (to Pharos) | always | fg |
| `repositories.merge` | Merge repositories (Terminal) · Merging repositories | cli (`repositories --merge --apply`) | cli | D:w W:w | an.identities, an.git | — | Pharos cannot stop it. | none (to Pharos) | always | fg |
| `tools.ledger.build` | Build the Tools ledger · Building the Tools ledger | user (`POST /api/tools/backfill`), version, step of `library.upgrade` | svc, cli (`build-tools`) | D:w W:w | an.ledger, an.tools | — | Stops between conversations; building the ledger again resumes it. | record | always | fg |

Notes:

- **Version catch-ups** (E4) are the C kinds above with trigger `version`.
- **Initialize and the journal.** The journal table is created by
  Initialize's schema step, so `catalog.initialize` writes its row (start
  and end together) after that step; a kill before it leaves no row. In
  the service, Initialize runs before the registry exists: it is timed and
  registered, already finished, when the registry attaches, and journaled
  if it took more than 2 s. A Terminal command takes its lease first and
  records Initialize as a live child of its operation (§6.5), so the
  service sees it while it runs. The broker separately records `service.start` (G), which
  covers the wait.
- **Upgrade steps.** `library.upgrade` plans the pending steps of
  `UpgradeSteps` (seven today, then the Tools rollup), so the journal
  answers "interrupted at step 5", which in-memory state cannot.

### 3.6 F. Protect and move

| Kind | Label · verb | Trigger | Proc | Holds | Affects | Stop | On stop or eject | Latency | Journal | Pri |
|---|---|---|---|---|---|---|---|---|---|---|
| `backup.local` | Back up · Backing up | user, cli | svc, cli (lease only) | D:w B(d):w C(\*):r (reads the library; writes its `backups.json`) | backup.local | backup (service only) | Stops; the next backup carries on from what was copied. Waits for each host's capture (`blocked(capture-running)`; today's phase "waiting for a capture to finish"). | record | always (service); never (CLI, lease only) | fg |
| `cloud.connect` | Test a cloud backup destination · Testing the destination | user | svc (inside the request, merged with `s.life.ctx`) | D:w K:w (a probe file in staging) | — | cloud | Stops the test; connect the destination again afterwards. | instant | always | fg |
| `cloud.upload` (variants `sample`, `full`) | Upload a cloud backup · Uploading a cloud backup | user, schedule | svc | D:w K(d):w (staging; reads a catalog snapshot) | backup.cloud:d | cloud | Stops the upload and deletes its staged archive; the last completed cloud backup is unchanged. It starts again from the beginning when started again, or at the schedule's next time. | batch (snapshot), record (upload: the S3 abort and the final save have 2 s limits each, then the staging is removed) | always | fg (`user`), bg (`schedule`) |
| `cloud.verify` | Verify a cloud backup · Verifying a cloud backup | user, cli (`cloud-backup verify`, required for password-protected backups) | svc, cli (catalog writer: journal and lease) | D:w K(d):w W:w (staging; saves the verification state) | backup.cloud:d | cloud (service only) | Stops and removes the partly recovered copy; run the verification again. | record | always | fg |
| `cloud.reconcile` | Record interrupted cloud backups · Recording interrupted cloud backups | read (`GET /api/cloud-backup` that finds a run whose process is gone), schedule (each schedule tick records such runs and settles the schedules of finished ones in a pass, discarded when it stored nothing) | svc | D:w W:w (and removes the run's staging) | — | — | Stops; the next Cloud page load or schedule tick records them. | record | never | bg |
| `cloud.restore` | Restore from the cloud (Terminal) · Restoring | cli (`cloud-restore`) | cli | — (a new location; out of scope) | — | — | **Out of status scope.** It restores to a new location, often on a Mac with no library or Pharos configuration (`cloud_cli.go`), so there is no library whose status could show it. It takes no lease and writes no journal. | — | never | fg |
| `library.transfer` (variants `copy`, `move`) | Copy library / Move library · Copying the library | user (Libraries page), cli (`library-transfer`) | brk, cli | O:w D(source):w D(target):w | (target) conv:\* | — | Not stoppable; eject of either drive must wait. | none | always (broker registry) | fg |
| `library.recover` | Restore a backup · Restoring a backup | user, cli (`library-recover`) | brk, cli | O:w D(target):w | (target) all | — | Not stoppable; eject must wait. | none | always (broker registry) | fg |
| `library.deliver` | Deliver captured history · Delivering history | schedule (every 5 min while a pair is set), device (target attached) | brk | O:w M:w D(source):w D(target):w (`M` while stopping children) | (target) conv:\* | — | Not stoppable; it stops both libraries' services first (§4.4). | none | always (broker registry) | bg |
| `library.deliver.capture` | Capture into the pending library · Capturing pending history | stage of `library.deliver` | brk | D(source):w | — | — | As its parent. | none | step | bg |
| `library.adopt` | Move this Mac's archive into a library · Moving the archive | onboarding, cli | cli, svc | D(source):w D(target):w | all | — | Not stoppable. | none | always | fg |
| `library.create` | Create a library · Creating the library | user | brk | D(target):w | — | — | Not stoppable; short. | none | >5 s (broker registry) | fg |

### 3.7 G. Process and device lifecycle

| Kind | Label · verb | Trigger | Proc | Holds | Affects | Stop | On stop or eject | Latency | Journal | Pri |
|---|---|---|---|---|---|---|---|---|---|---|
| `service.start` | Open the library · Opening the library | user (select), startup, respawn | brk | M:w D:w (`M` today, for up to 2 min) | — | — | Not stoppable. The broker gives up after 2 min and SIGTERMs the child (§10, G2). | none | always (broker registry) | fg |
| `library.release` | Release the library · Releasing the library | user (eject, quit, switch through the API), device (volume release), signal (SIGTERM/SIGINT), parent-exited, error | svc, brk | D:w | — | — | Is the stop. Fails after 7 s with "Pharos is finishing a write". | — | always | fg |
| `app.update` | Update Pharos · Updating Pharos | schedule, user (Sparkle) | app | update-lock:w (native) | — | — | Native; bridged to the header ring as today. | — | never (Sparkle keeps its own) | bg |

Not kinds: the broker's 5 s maintain loop (a daemon whose respawns are
`service.start` with trigger `respawn`), the native 5 s state watch, the
service's watchdogs (parent gone, drive gone, SIGBUS), DiskArbitration
monitors, the quit gate (a consumer of the eject verdict), MCP stdio servers
themselves (only the calls they answer directly, §6.5), and service log
capture.

## 4. Holds and the eject verdict

### 4.1 Holds

A hold names a resource and a mode. Resources: `drive` (the library's
volume), `drive:<path>` (another volume: a backup destination, a transfer
target), `catalog-writer`, `ingest`, `capture:<host>`, `index:<capture>`,
`backup-dest:<path>`, `cloud:<destination>`, `broker-operation`,
`initialize`, `broker-mutex`.

Holds are declarations for status. In phases 1–3 they change no locking:
the code keeps taking today's 13 locks, and the hold says which of them the
operation takes. A test asserts that each kind that takes a lock today
declares the matching hold.

**Concrete lock holds.** A kind row's `C(...)`/`X(...)` holds are generic.
An operation gains a concrete `capture:<hostID>` or `index:<host>/<source>`
write hold only once it has actually taken that lock file
(`Op.holdLock`, at `beginCapture`'s callers, `lockCaptureTarget` and
`IndexCapture`), and loses it when it releases the lock: every such lock is
closed through `releaseLockFile`, which drops the hold and bumps the
operation's version (journal and snapshot). A Terminal command rewrites its
lease when it takes a lock, before writing under it; a release is written
on the journal writer's next tick (at once for a command without a
journal), and a rewrite that would not change the lease's locks is
skipped. Concrete
locks are kept apart from declared holds: an operation's view carries them
as `Locks`, a lease as `locks`, and only they name a held lock. A lease's
declared holds resolve `C(this)` to this Mac's host ID but never name a
lock.

### 4.2 The verdict

The eject verdict for a library is computed on the server from the
operations that hold its `drive` (read or write) and are known to this
Mac:

1. **this service's registry**, for its own operations. The registry is
   authoritative for live state in its own process; the verdict never reads
   this process's journal rows, which may lag by seconds (§6.2);
2. **process leases** of other Pharos processes on this Mac (§6.5), listed
   and probed synchronously on each verdict read: a held lease is a live
   holder with the kinds and holds it declares; journal rows only add
   history and progress to it;
3. the broker's registry entries that touch the library.

| Verdict | When | What eject does |
|---|---|---|
| `safe` | No running operation holds the drive for writing. Read-only holders (a `maintenance.cache-warm` pass) may run; eject stops them | Release stops any readers (each `on_eject` says how) and closes the catalog |
| `safe-in(eta)` | At least one operation holds the drive for writing, and every drive holder stops at a safe point within the release budget (7 s, `stopTimeout`): latency `instant` or `record`, a `SafePoint` estimate ≤ 7 s, or a `batch` holder with no estimate (assumed to stop in time; a release that times out refuses safely). `eta` is the largest estimate, or `null` when any is unknown | Release stops them at safe points and closes the catalog. With an `eta`: "Ejecting stops <label> within about N s". With `eta: null`, fixed wording: "Ejecting stops <label> after the current <unit>; if that takes more than 7 s, Pharos refuses and you can try again" |
| `busy` | Some holder blocks release: a held lease of another process (CLI work), a broker operation, `catalog.initialize`, a `release-blocking` request-scoped operation (§2.3), or a holder whose `SafePoint` estimate exceeds the budget or whose latency is `step` or `none`; `eta` is its safe point or forecast when known | Eject is refused until it ends or reaches its safe point; the panel names it |
| `unsafe` | Unknown or unregistered writer evidence: kind `unknown`; unregistered running work found by the registry's self-check; a held capture or index lock that no live lease and no operation in this service's registry names by an exact concrete write hold (§4.1; holding the ingest coordinator names nothing); a lease file or lease directory that cannot be read, listed or probed; the service unreachable while the drive is mounted | Eject is refused; the panel says what is unknown |

So a Search view refresh alone (`maintenance.library-view`, a `record`
writer) is `safe-in`, never `safe`.

**Polled and fresh verdicts.** Writers take the capture and index locks
with `LOCK_EX|LOCK_NB`, so even a brief shared probe can make a real capture
or index fail. Every capture and index lock is therefore probed only by a
**fresh** verdict (`GET /api/operations?fresh=1`, `ejectVerdictNow(ctx,
true)`): the release and quit path, where stopping work is the intent. Any
read error in that scan (the capture root, a host directory, a lock file)
is an `unsafe` blocker; a missing root is empty. A **polled** verdict (the
default snapshot, the broker's document) takes no lock at all: it reuses
the most recent result of today's `captureActive` probe of this Mac's
capture lock, taken by the `/api/library/status` path, if that result is
under 5 s old, through the self-check; with no recent result it does not
know about such a capture. The verdict carries `checked: "fresh" | "polled"`;
a polled verdict that would be `safe` says "Nothing Pharos can see is
writing to the library. Pharos checks everything again when you eject."
Phase 2's Eject button requests a fresh verdict before it acts. Lease files
may be probed from any read: a creator's `LOCK_EX` blocks briefly rather
than failing.

Defaults for a holder with no `SafePoint`: `instant` stops within 1 s,
`record` within 2 s, `batch` has no estimate; a `queued` writer holds
nothing yet and stops within 1 s. `library.release` is the stop itself,
not a holder. Operations another process runs (a live lease, or its journal
rows while its lease is held) are `busy` whatever their kind's latency.
The **self-check** compares the registry with `libraryActivities`: a writer
it lists that no running operation or live lease accounts for is
"unregistered running work", and makes the verdict `unsafe`.

**Tentative passes** (§3.4). A daemon pass is registered before it looks
for work, every 2 s while idle. A **polled** verdict counts a pass that is
still tentative only while it may be writing: a tracked write transaction
is open in this process (`writerActive`) or `libraryActivities` lists a
writer. Otherwise an idle library would flicker between `safe` and
`safe-in`. A **fresh** verdict always counts it. A pass that has done a
unit of work is published and counts like any operation. Surfaces may
also hold a safer verdict back briefly (the panel: 2 s) but never delay a
less safe one.

Rules:

- **What "holds the drive for writing" means.** An operation holds the
  drive for writing when it declares a `D:w` hold **or any** `W:w` hold
  (the catalog writer): the catalog is on the drive, so a catalog write is a
  drive write, even when the kind's `D` hold is a read. Only an operation
  whose every hold is `:r` is read-only.
- **Err toward not safe.** When two rules apply, the worse verdict wins
  (`unsafe` > `busy` > `safe-in` > `safe`). A missing estimate never improves
  a verdict.
- **Read holds still count as blockers.** A read-only holder does not make
  the verdict worse than `safe`, but ejecting stops it and the snapshot
  lists it with its `on_eject`. A local backup is not read-only: it writes
  the library's `backups.json` (its id on the first backup and each
  finished backup, `backup.go`), so it holds the drive for writing.
- **Known gap: older builds.** A `pharos index` or `ingest` from an older
  build on this Mac takes no lease and holds no lock the service probes, so
  it is invisible, as it is to `libraryActivities` today. Probing SQLite for
  other writers would itself be a write attempt on a status read, so the
  contract does not; the gap closes once every writing command takes a
  lease.
- **Unplug without eject** stays as today: `probably-fine` only when there
  is no holder at all; otherwise `unsafe`.
- **The catalog is always open** while the service runs; that alone never
  makes the verdict worse than `safe`.
- **Blockers** in the snapshot list every holder with its label, `on_eject`
  and latency, worst first.

### 4.3 Today's equivalent

Until phase 2 switches surfaces, `libraryActivities` remains the answer the
UI uses (`idle`, `safe_to_unplug`, `unplug`). Phase 0 added in-service cloud
work to it. The parity invariant: the shadow verdict is never `safe` while
`libraryActivities` lists a writer. The phase-1 parity test checks it, and
also asserts over the kinds table that no kind with a `W` hold, and no kind
whose `D` hold is `:w`, is classified as read-only.

### 4.4 Broker operations and other libraries

The broker's verdict for a library also considers broker operations naming
it. The delivery cycle stops (SIGTERMs) the services of both libraries in
the pair before it captures and transfers (`deliveryCycle`,
`application_jobs.go`); while it runs, both libraries are `busy`, with the
blocker "Delivering history".

## 5. Freshness domains

Phase 3 implements these as one function over today's storage
(`Catalog.freshness`, `freshness.go`). Storage changes only by two kinds of
additive `meta` rows that record what a completion was for (below). Every
row has:

- `domain`, `group` (`conversations`, `search`, `analyses`, `findings`,
  `upgrade`, `backups`), `label` and `state_label` (server words);
- `as_of`: when the domain was last brought current (RFC 3339), or null
  when that is not recorded (the search indexes) or it never was;
- `behind`: `{"state": "current" | "scheduled" | "behind" | "unknown", "count": N, "unit": "…"}`,
  with `count` and `unit` only when the tables count something real.
  `scheduled` is a domain waiting for its next planned run within its
  designed cadence (today: findings whose inputs changed after today's full
  pass, which runs once a day); past its cadence it becomes `behind`
  (findings: `daily-pass-due`). It is not staleness. Analyses deferred by
  scheduled indexing are not `scheduled`: they are `behind`;
- `reason`: `{"code", "text", "detail"}`, one code from the list below with
  its server sentence (`detail` names, for example, the failed stage), or
  null when current;
- `catching_up`: the ID of the queued, running or blocked operation whose
  kind's `affects` (§3) covers the domain (the most recently started, so a
  chain stage before the index that started it), or null; with
  `catching_up_label`, its label. Tentative daemon passes are not counted.
  It is set only when the domain is not current;
- `next`: what will catch it up when nothing is running, in server words
  ("updates with the next scheduled index", "updates with the next manual
  index", "carries on by itself"), and `next_at` where a schedule says
  when. Null while an operation is catching it up.

**The governing rule: `current` needs positive evidence.** A domain is
`current` only when there is a recorded completion at or after the last
relevant change of its inputs. The absence of a "behind" marker is not
evidence. Without that evidence the state is `unknown`, with its `as_of`
and an honest reason. The evidence per domain:

- conversations: the scheduled index's successful check within its
  interval (it read the source);
- captures: `capturedHosts` finds the source indexed after its newest
  captured data, at the current indexer version;
- the Search view and the search indexes: their trigger-maintained dirty
  set is empty, or their done flag is set (the triggers keep them current
  after that), at this version;
- the Tools rollup and writing classification: their built generation
  equals the generation they are built from (the existing rebuild rules,
  which record the generation read before the rebuild);
- identity links and Git merges: `meta['analysis_inputs:<stage>']` equals
  the record generation, `records:<authorship_inputs_generation>` (which
  only ingest moves, with every write of conversations; these stages move
  the tool ledger's generation themselves, so it is not their evidence),
  and nothing is deferred. One helper (`completeStage`) runs the stage for
  every caller (the chain, `pharos ingest`, the legacy source sync, capture
  indexing, history verification, the Git backfill), snapshots the record
  generation *before* the stage, and records that value on success: a write
  during the stage leaves the domain `behind / changed`, never falsely
  current. The record is written in the journal's low-priority lane: an
  idle connection, the writer gate without waiting, no busy timeout, one
  attempt; when the writer is busy it is skipped (logged once), which
  leaves the domain behind or unknown, the honest direction;
- findings: today's full pass, and `meta['findings_inputs_generation']`
  (`tool_ledger_generation:authorship_inputs_generation`, read before the
  pass and written with its other meta) equals the current value, and
  nothing is deferred. The pass's day is fixed when it starts, so a pass
  that crosses midnight records the day it analysed;
- the tool ledger and the upgrade: `meta['upgrade_count']`
  (`{build, started_at, steps}`) was counted by the build that last
  initialized the catalog (`meta.initialized_build`). This build's ingest
  cannot create upgrade work; only a write by another build can, and that
  build's open changes `initialized_build`. It is counted at the end of the
  analysis chain, after an upgrade completes, and once the service is
  serving, in the background as `maintenance.upgrade-count` (§3.4), only
  when the stored count is not this build's (`buildDigest()`); Initialize
  never counts. Until a count lands the ledger says `unknown / not-counted`.
  An answer is never replaced by one whose count started earlier. Every
  process (the MCP one too) reads the same row;
- backups: a verified (cloud) or complete (local) backup within an enabled
  schedule. Without a schedule, the newest backup is reported `unknown`
  (`no-schedule`) with its `as_of`; local backups have no schedule.

**Nothing to process is evidence.** A library with no conversations gives
the derived domains (the search indexes, the analyses, the tool ledger,
findings and the upgrade) no inputs, so nothing waits: each is `current`
with reason `empty` ("Nothing to analyze yet", "Nothing to index yet",
"Nothing to upgrade yet"), the one reason a current row carries. A query
that fails still makes a domain `unknown`, and for the tool ledger and the
upgrade a stored count wins. An empty library with no sources reads "All
current".

Tooltip lines drop a part they already have: a reason whose words say what
catches the domain up is not followed by the same `next` again.

Catalogs from before phase 3 have no `analysis_inputs:*` or
`findings_inputs_generation` rows: those domains are `unknown`
(`no-record`, "Not checked since this version of Pharos; updates with the
next manual index") until the next manual index (or findings' next full
pass). This is transitional and expected.
An older Pharos ignores the rows. The ledger's other storage is one
additive index, `operation_journal_kind (host_id, kind, started_at DESC)`.

| Domain | `as_of` from | Behind measured by | Reason codes (in order after the running code) | Caught up by |
|---|---|---|---|---|
| `conv:<source>` (this Mac's enabled sources) | max of `automatic_source_states.indexed_at`/`succeeded_at` and `source_states.last_success_at` for this host | count unknown | `indexing`, `never-indexed` (behind), `interrupted`, `source-error` (the latest attempt, across both tables), `capture-busy` (the latest index left it to a running capture), `needs-capture` (behind: `source_input_states.pending`), `schedule-off`, `overdue` (the schedule's last success is older than max(2 × interval, interval + 15 min)); all but the two marked are `unknown` | `index.changes.*`, `library.update`, `index.sources.legacy`, `index.captures`, `library.rebuild` |
| `capt:<host>/<source>` | the source's `source_states.last_success_at` on that host (when its captures were last indexed) | `needs_index` from `capturedHosts`; count unknown | `indexing`, `capture-running` (a `capture.*` operation is the one catching up), `unreadable` (the capture root cannot be read: the rows last read, all `unknown`, or one `capt:*` row when none were read; a root that does not exist and never had captures has no rows), `not-indexed` (detail: `capturedHosts`' `index_reason`) | `index.captures`, `library.update`, `capture.*` |
| `view` (Search view rows) | — | `COUNT(*)` of `workspace_library_dirty` (capped at 100,000), unit workspace | `refreshing`, `version` (`workspace_library_version`; count unknown), `stopped` (`libraryRefreshStopped`) | `maintenance.library-view` |
| `search.substring` | — | conversations past `message_trigram_cursor` (counted only while not ready) | `building`, `version` | `maintenance.search.substring` |
| `search.text` | — | `text_search_documents_version` not `1`; count unknown | `building`, `version` | `maintenance.search.text` |
| `search.tools` | — | `tool_command_fts_version` not `1`; count unknown | `building`, `version` | `maintenance.search.tools` |
| `search.query` | — | the three `*_ready` keys not at their index's version; count unknown | `building`, `version` | `maintenance.search.query` |
| `an.identities` | the later of `sync_deferred[identities].completed_at` and its latest succeeded journal row | count unknown: `sync_deferred` generations count written records, not runs | `running`, `failed` / `blocked` (below), `deferred-by-schedule`, `no-record` (unknown), `changed` (the inputs moved since its completion: a write outside the chain, such as a Terminal ingest) | `analysis.chain`, `analysis.identities`, `analysis.github` |
| `an.git` | as above, for `git` | as above | as above (`git-unavailable` is not detected: the 6 h retry timers are in memory) | `analysis.git`, `analysis.chain`, `analysis.refresh` |
| `an.tools` (Tools rollup) | as above, for `tools` | `COUNT(*)` of `tool_rollup_dirty` (capped), unit workspace, when that is why | `running`, `failed` / `blocked`, `deferred-by-schedule`, `never-built`, `version` (an older layout), `changed` (a newer ledger generation, dirty workspaces, another day or timezone: the `toolRollupStale` rule) | `analysis.tools`, `library.upgrade`, `tools.ledger.build` |
| `an.authorship` | `authorship_built_at` | `authorship_generation` ≠ `tool_ledger_generation:authorship_inputs_generation/version`; count unknown | `running`, `failed` / `blocked`, `deferred-by-schedule`, `never-built`, `changed` | `analysis.authorship` |
| `an.ledger` (tool ledger) | when it was last counted | the upgrade's `tools` step count, from the last `UpgradeSteps` | `running`, `not-counted` (unknown: not counted by this build), `version` | `tools.ledger.build`, `library.upgrade` |
| `findings` | `findings_built_at` | count unknown | `running`, `failed` / `blocked`, `never-built`, `deferred-by-schedule`, `daily-pass-due` (`findings_generation` ≠ `findingsVersion/<today>`), `no-record` (unknown: today's pass ran on an older build), `changed` (`scheduled`: new data since today's full pass). Both say what runs the next full pass: "updates with Refresh findings now, or the first index tomorrow", or with scheduled indexing on (which defers findings) "…, or the next manual index", and the muted short "updates tomorrow" / "updates with manual index" | `analysis.findings`, `analysis.chain` |
| `upgrade` | when it was last counted | steps with pending units, from the last `UpgradeSteps` | `running`, `not-counted` (unknown), `upgrade-available` | `library.upgrade` |
| `backup.local` | the newest non-partial backup in `backups.json` | age only | `running`, `failed` (this process's latest backup failed after `as_of`), `never` (unknown), `no-schedule` (unknown, with `as_of`) | `backup.local` |
| `backup.cloud:<destination>` | the destination's newest complete, verified full run | against an enabled schedule: older than its cadence + 12 h | `running`, `retrying` (enabled, `failures > 0`), `failed`, `unverified`, `never`, `overdue`, `no-schedule` (unknown, with `as_of`); `paused` replaces any but `failed` when the schedule exists and is off | `cloud.upload`, `cloud.verify` |

Rules:

- **The running code comes first.** When an operation is catching a domain
  up, its reason is that domain's running code (`indexing`, `refreshing`,
  `building`, `running`, or `capture-running`), whatever the stored state
  says, so "indexing now" beats an earlier `source-error`. Otherwise
  `reason` is the first applicable code in the row's list.
- **Failure precedence.** The chain runs github, identities, git, tools,
  authorship, findings, and stops at the first failure. For an analysis
  that is not current, its own stage's latest journal row having failed
  after its `as_of` (and after the deferral's `changed_at`, when scheduled
  indexing deferred it) makes it `failed`; an earlier stage's such failure
  makes it `blocked`, with that stage's label as `detail`. Both outrank
  `deferred-by-schedule`. A domain whose evidence says current stays
  current whatever failed before.
- **No misleading counts.** A count is given only when it counts the unit
  it names (workspaces, conversations, steps). Scheduled deferral is shown
  as behind since `as_of`, with no count.
- **Cheap.** It runs on every snapshot computation: one `meta` query for
  every key, `sync_deferred` (5 rows), the source state tables for this
  host, two counts capped at 100,000, `cloud_destinations`, two indexed
  queries per destination (its latest finished and latest verified full
  run, through `cloud_runs_destination`) and the schedules, and the
  journal's latest outcome and latest success of each chain stage, two
  lookups per stage through `operation_journal_kind`. It never probes a
  lock. The file-derived inputs (`backups.json`,
  `capturedHosts`' capture manifests) are cached for a minute per capture
  root and backup history, and read again sooner (not more than once per
  2 s) when an operation affecting `capt:*` or `backup.local` starts or
  ends. The files are read outside the cache's mutex, by one reader at a
  time (its flag cleared however the read ends); every other reader takes
  the last completed value at once. The
  release path's fresh check (`?fresh=1`) never reads or waits: it takes
  the cache for the same configuration, or reports those domains
  `unknown`. The upgrade's count is the stored row above. Measured: about
  0.25 ms per computation on a test catalog with 2,000 rows in each dirty
  table.
- **Summary.** `/api/status` also carries `freshness_summary`:
  `{groups, all_current, text, checked_label, checked_at}`. Each group
  present is `{group, label, state, short, short_age, text, as_of,
  as_of_label, tooltip, catching_up, shown, muted}`. Its state is the worst
  of its rows (`behind` over `unknown` over `scheduled` over `current`). A
  `scheduled` group's `short` is "updates <when>" ("updates tomorrow"). `short` is the Data row's
  word: `failed` (a row failed, is blocked or retrying), `updating` (every
  row that is not current is being caught up), `waits for manual index`
  (every uncaught behind row's `next` is the next manual index), `behind`
  (with its age when `short_age`), or `unknown`. `text` is the longer
  phrase for tooltips: "catching up" only when every row that is not
  current is caught up, otherwise the state with the first uncaught row's
  `next`. `as_of` is the least recent among the rows in that state;
  `tooltip` lists the rows that are not current, why, and what catches them
  up. The Data row answers "is the data on my pages current?", so it covers
  only the groups that feed pages: conversations (with captures), search,
  analyses, findings and upgrade. `shown` is true for those that are
  `behind` or `unknown`; `muted` for those only `scheduled`. Backups are
  never shown: they stay in the ledger, Show all and MCP, and reach the
  panel only through attention (§7.3 rules 6 and 7: a failed backup, a
  failed scheduled cloud backup). With none shown, `all_current` is true,
  `text` is "All current" and `checked_at` the least recent check of this
  Mac's conversations; `scheduled` and backups never prevent it. The
  panel's Data row is one line (two on a narrow screen): the shown groups
  as "<label> <short>", or "All current · checked <age>" followed by any
  muted groups ("Findings updates tomorrow"); it adds only ages.
- **The three older Go rules stay** (no retirement in this series):
  `Catalog.Freshness` (`catalog.go`) ignores scheduled indexing, MCP
  `archiveSourceStatus` (`mcp_audit.go`) counts it but calls a source with
  no manual index "unknown coverage" and ignores whether scheduling is on,
  and the menu's `indexed_at` (`menu_bar.go`) is the last write, not the
  last check. `TestFreshnessDivergesFromTheOlderRules` records where each
  disagrees with the `conv:<source>` rule above, for the phase that
  retires them.
- **MCP.** `get_archive_status` keeps its fields and adds `domains` (each
  row compact: `domain`, `state`, `as_of`, `count`, `unit`, `reason`,
  `reason_text`, `catching_up`, `next`) and `running` (the operations
  affecting them: `id`, `kind`, `label`, `state`, `started_at`,
  `domains`). It is additive: the page is fitted first, exactly as without
  the ledger, and the ledger takes only the budget left, dropping the
  current domains (`domains_partial`), then itself (`domains_omitted`, only
  when that marker fits). The ledger is not computed when less than about
  300 bytes of budget are left. The MCP process caches its capture
  manifests per catalog for a minute. The
  MCP process has no registry or configuration: its sources are those the
  state tables know for this host, its operations are this host's open
  journal rows whose process is alive and started no later than the row
  (so a reused PID does not count; without a start time the row is not
  counted), work shorter than its kind's journal threshold is not there
  yet, and the local backup is `unknown`.
- Freshness is per library and this Mac's sources only; other Macs' captures
  appear as `capt:<host>/<source>` for indexing, not as conversations.

## 6. Journal

### 6.1 Table

Added to `internal/archive/assets/schema.sql`, additive, with no
`catalogSchemaVersion` bump. An older build ignores the table.

```sql
CREATE TABLE IF NOT EXISTS operation_journal (
  id TEXT PRIMARY KEY,               -- 32 hex characters, random
  parent_id TEXT,                    -- NULL for a top-level operation
  root_id TEXT NOT NULL,             -- the top-level ancestor (its own id at the top)
  kind TEXT NOT NULL,
  variant TEXT NOT NULL DEFAULT '',
  subject TEXT NOT NULL DEFAULT '',
  trigger TEXT NOT NULL,
  host_id TEXT NOT NULL,
  process TEXT NOT NULL,             -- service | cli:<command> | mcp
  pid INTEGER NOT NULL,               -- informational; liveness comes from leases (§6.5)
  instance TEXT NOT NULL,            -- random per process lifetime; names its lease file
  state TEXT NOT NULL,               -- §2.2
  reason TEXT,                       -- blocked or skipped reason, or a failure class
  error TEXT,
  step_index INTEGER, step_count INTEGER, step_name TEXT, step_unit TEXT,
  step_done REAL, step_total REAL,   -- step_total NULL = unknown
  started_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  ended_at TEXT,                     -- NULL while open
  legacy_ref TEXT,                   -- SyncRun ID, BackupRun ID, cloud run ID…
  detail_json TEXT NOT NULL DEFAULT '{}' -- holds, affects, priority, counters, plan
);
CREATE INDEX IF NOT EXISTS operation_journal_open ON operation_journal(host_id) WHERE ended_at IS NULL;
CREATE INDEX IF NOT EXISTS operation_journal_recent ON operation_journal(host_id, started_at DESC);
CREATE INDEX IF NOT EXISTS operation_journal_root ON operation_journal(root_id);
```

**Retention.** Keep every open row. Of ended rows, keep 30 days and at most
2,000 per host, whichever is fewer; children go with their root. Pruning
runs at service start and then daily, in one small transaction, for this
host's rows only.

**Copies and transfers.** The journal is not history: it is not in
`transferHistoryTables` or `moveRetainedTables` (`library_transfer.go`),
so library copy, move and delivery merges never carry it. Backups and cloud
backups copy the catalog file whole, so a restored copy carries its rows;
its first service start reconciles this host's open ones (no lease holds
them, §6.5) to `interrupted`.

### 6.2 Write policy: a low-priority lane

**The registry is live state; the journal is the record.** The in-process
registry is authoritative for the live state of its own process's
operations; snapshots, the eject verdict and stop wiring read it. The
journal is the durable record and the cross-process history, and it may
lag by seconds.

- **One writer per process.** A journal writer goroutine holds two kinds of
  pending entry:
  - **row images**, keyed by op ID, from this process's own operations.
    Handles only replace an entry; they never block, never touch SQLite,
    and never fail;
  - **conditional reconciles**, keyed by op ID, from the reconcile pass
    (§6.5): a target state, reason, outcome and `ended_at` for a row owned
    by another instance, written as `UPDATE … WHERE id=? AND instance=?
    AND ended_at IS NULL`. A reconcile never becomes a row image and never
    overwrites a row its owner has ended.
- **Never ahead of ingest.** Journal writes must not queue on the catalog
  writer gate (`acquireWriteGate`, `catalog_contention.go`) ahead of real
  work. The writer runs a round at most every 3 s and takes nothing it would
  have to wait for while it holds the gate: first an idle pooled connection
  (waiting at most 5 ms, holding nothing), then the gate without waiting (a
  non-blocking send on the gate channel); if the gate is taken, or the
  process has a write transaction in flight, it skips the round and keeps
  the pending images. With the gate it writes at most 64 rows in one tiny
  transaction (`BEGIN IMMEDIATE` with `busy_timeout` 0, so another process
  holding SQLite's lock makes it skip at once), gives the gate back (rolls
  back and skips) as soon as a tracked writer starts waiting for it, and
  never retries while holding it.
- **Ingest writers are gated.** Every write on the ingest path goes through
  the tracked gate (`beginTrackedWrite`), including the part-state flush,
  the preview fill and `repair-existing`'s per-record writes, so the journal
  yields to them. **Residual:** a writer that bypasses the gate outside
  ingest (a handful of small direct `DB.Exec` writes, such as recording a
  source's state), or a gated writer that arrives after the last yield
  check, can wait for one short journal commit; a writer in another process
  meets the journal like any other SQLite writer. A round has a 250 ms
  deadline. Measured on an internal SSD (`TestJournalGateHoldIsShort`, 20
  rows per round, 200 rounds): the gate is held p50 0.6 ms / p95 0.7 ms
  with the tests' durability, and p50 5.2 ms / p95 6.2 ms (max 9.2 ms) with
  the service's (`fullfsync`); an external drive's fsync will be slower.
- **No starvation.** Every round first tries the normal, non-blocking way.
  Only when that skips, and the 9 rounds before it skipped too (a writer
  keeps the gate saturated), does the same round queue for the gate in its
  normal FIFO order (waiting at most 2 s) and write only starts and ends
  (rows never written, and ended operations; at most 64): no progress, no
  reconciles, never the prune; it does not yield, and releases. The count
  resets on a normal round that writes, or when nothing is pending.
- **Starts and ends can wait.** A start or end waits for the next round that
  gets the gate; under a long ingest that may be a few seconds, because
  ingest commits per workspace and frees the gate between commits. A row
  that has waited 30 s is still written only through the same non-blocking
  rule; nothing escalates to a blocking write.
- **Coalescing.** Each round writes the latest image per op (and each
  pending conditional reconcile once), so step
  changes, blocks and progress between rounds collapse into one write.
- **Drops.** A round that fails (`no such table`, read-only, drive gone) is
  dropped and counted in the snapshot's `journal.dropped`; the next round
  writes the latest images.
- **`if-longer-than(T)`.** The start row is deferred until the op has run T
  or called `Wrote()`; an op that ends before either writes nothing. Its
  row, once written, follows the normal rules.
- **Never per record.** No journal write on the ingest hot path; `Advance`
  inside ingest only updates memory.
- **Flush on release.** `Server.stop` waits for workers as today, then runs
  one final round with a budget of `min(1 s, what is left of the 7 s release
  budget)`, skipped when nothing is left, then closes the catalog. A stop
  that has already timed out (the release answered 503) still runs that
  round, with a 1 s budget, before it closes the catalog; the final round
  may wait for the gate, since nothing else is writing by then.
- **No journal on read-only catalogs.** `OpenCatalogForQuery` catalogs
  (`queryOnly`), MCP read paths and `immutable=1` opens have a nil writer;
  their handles are in-memory only.

### 6.3 Ownership

A row belongs to its `instance`. Only the owning process updates an open
row, except for the reconciliation in §6.5. There is no heartbeat: liveness
comes from leases, not from `updated_at`.

### 6.4 The service's own rows after a restart

When a service starts, after it holds the exclusive service lease
(`.pharos-service.lock`, `Server.Serve`) and Initialize has finished, every
open row with `process = service` and another `instance` belongs to a
service that is gone (two services cannot hold the lease): it becomes
`interrupted` with reason `owner-exited`, and its open children follow.
Rows with another `host_id` are never touched.

### 6.5 Other processes: leases

Liveness of other processes is never inferred from pids or heartbeat rows.

- **Every non-service Pharos process that runs a structural operation**
  (`capture`, `index`, `ingest`, `repair-existing`, `backup`,
  `cloud-backup verify`, `history-backfill`, `upgrade`, `build-tools`,
  `library-transfer`, `library-recover`, `add-this-mac`, adoption, and MCP
  servers for each call they answer directly)
  holds an exclusive `flock` on its own lease file for as long as it runs
  that operation.
- **Where:** `<catalog dir>/.pharos-leases/<instance>.json`, or
  `<capture root>/.pharos-leases/<instance>.json` for a command that never
  opens the catalog (`capture`, `backup`), so the service lists both
  directories.
- **Contents:** JSON with `instance`, `pid`, `host_id`, `process`
  (`cli:<command>`, `mcp`), `kinds`, `holds`, `started_at`, `operation`
  IDs when it journals, and `outcomes` only when its final journal round
  could not write (below).
- **Creating a lease.** The process opens the path with `O_CREAT|O_RDWR`,
  takes `LOCK_EX` (blocking is fine: only a deleter holds it, briefly),
  then checks that the locked file is still the one at the path (`fstat`
  of the fd against `stat` of the path: same device and inode). If not (a
  deleter unlinked it in between), it closes and starts again. Only then
  does it write the contents in place (`ftruncate` + `pwrite`, `fsync`).
  Updates are written the same way, in place. A lease is **never** replaced
  by rename (`durableJSON`/`writeFileAtomic` must not be used for it): a
  new inode at the path would be unlocked. The process keeps the fd open
  and locked until its operations end, then unlinks the file while still
  holding the lock, then closes it.
- **Deleting a dead lease.** The deleter opens the path, takes
  `LOCK_EX|LOCK_NB`; if that fails the lease is live. If it succeeds, it
  applies any recorded outcomes first (below), then unlinks the path while
  holding the lock, then closes. A creator racing it
  either sees the inode mismatch and retries, or locks a file that is not
  unlinked afterwards.
- **The flock is the liveness signal.** Pid reuse does not matter. Every
  verdict read (the snapshot, the stream, the release and quit-gate paths)
  lists the lease directories and probes each file synchronously: a
  read-only `open` plus `LOCK_SH|LOCK_NB` (held = live), as `captureActive`
  probes the capture lock today. Probing writes nothing. A burst of reads
  may share one probe result for at most 1 s; the release path always
  probes afresh. A GET never deletes a file or writes a row.
- **Lease lifetime.** A process takes its lease before it opens the
  catalog (so its Initialize, recorded as a live child of its operation, is
  seen) and keeps it until its last write: at exit it runs a final journal
  round with a bounded budget (1 s, through the same non-blocking rule plus
  one blocking attempt within that budget), closes the catalog (its
  checkpoint included), and only then unlinks the lease (while holding its
  lock) and closes it. A command that cannot take its lease has opened
  nothing. Read commands that open the catalog (`search`, `health`,
  `pricing`, `findings` without `--refresh`, `upgrade --status`…) take a
  lease for the open, Initialize and close (`runLeasedCatalogCommand`),
  since `OpenCatalog` and `Close` may write. An MCP server that answers a
  call itself (no service answering) leases it the same way, as
  `maintenance.mcp-history` with `catalog.initialize` declared: from before
  it opens the catalog until it has closed it, after the answer is out,
  which stores the call's history and may checkpoint. The lease names no
  host, since looking this Mac up can wait for `ioreg`: it still counts for
  the verdict (only a process on this Mac can hold its flock) but is not
  listed. It is written without `fsync`, as the flock alone is its
  liveness and a crash leaves a dead file whatever it holds. Writes fail
  closed, reads degrade: a call that cannot take its lease (an unwritable
  lease directory) still answers, from read-only connections, so it runs
  no Initialize, stores no history and never checkpoints. A call holds at
  most one lease.
- **When the final round cannot write.** The process writes its terminal
  outcomes into its lease file in place (`ftruncate` + `pwrite`, `fsync`;
  it still holds the lock): an `outcomes` map of op ID to state, reason,
  error and `ended_at`. Then it releases the lock **without unlinking** the
  file, and exits. The outcome survives in the unheld lease.
- **Reconciling in the background.** A reconcile pass runs on its own timer
  (every 10 s), independent of the writer gate and of the journal writer's
  rounds. For each unheld lease file it takes `LOCK_EX|LOCK_NB` only long
  enough to read any `outcomes` (so a verdict probe meanwhile does not take
  it for live), queues each as a conditional reconcile (§6.2) with that
  recorded state, and once those have been written claims it again and
  deletes it (the protocol above). It skips another Mac's leases, but
  takes one without a host (a direct MCP call's) for this Mac's. The
  broker deletes dead leases in the app lease directory, which have no
  journal rows, every 10 s.
  Then it reads the open rows on this host whose `instance` is not the
  running service's, and for each one probes **that row's own** lease
  (`<instance>.json`) at that moment; missing or unheld means dead. A dead
  row with an outcome in its unheld lease gets that outcome. Only a dead
  row with **no recorded outcome** gets `interrupted` with reason
  `owner-exited`. Every reconcile is a conditional update, never a full row
  image: `UPDATE operation_journal SET state=?, reason=?, error=?,
  ended_at=?, updated_at=? WHERE id=? AND instance=? AND ended_at IS NULL`,
  written through the low-priority lane. A row its owner ended in the
  meantime is left alone.
  This covers rows whose lease was deleted before their reconcile landed (a
  service killed in between, a dropped round) and rows from a previous
  service instance. Open children of a reconciled row follow with the same
  conditional update.
- **Journaling:** commands that already open the catalog writable (`index`,
  `ingest`, `repair-existing`, `upgrade`, `build-tools`, `add-this-mac`, and
  `cloud-backup verify`, which saves the verification state through
  `openCatalog` in `cloud_cli.go`) also journal their history, through the
  same low-priority writer, and keep their lease beside the catalog.
  Commands that do not (`capture`, `backup`) use only the lease, under the
  capture root; they must not start opening the catalog to report status.
- **Display:** a lease's operations are listed with "in another process
  (pharos capture)", from the lease's kinds and, when present, the journal
  rows' steps. They are never stoppable from Pharos.
- **Fallback:** on the release path, a capture or index lock held with no
  live lease and no operation in this service's registry naming it exactly
  makes the verdict `unsafe` (an older build's CLI, or a crash between
  taking the lock and writing the lease).
- **Fail closed.** A lease directory that exists but cannot be listed makes
  the verdict `unsafe` (a missing one is empty). A writing command that
  cannot take its lease does not run: it exits with an error saying nothing
  was changed.
- **App-level commands.** `library-transfer` takes a lease beside both
  libraries' catalogs and in `pharosSupportDir()/.pharos-leases`;
  `library-recover`, whose destination is new, only in the latter. Their
  leases name the library configurations they write (`libraries`). The
  broker lists live leases there as app operations, and makes each named
  library `busy`; for a library with no running service it also reads that
  library's own lease directories (never its capture or index locks).
- `cloud-restore` to a new location is out of status scope (§3.6).
- **Phase 1 coverage.** Leased: `capture`, `backup`, the writing
  `sqlite-records` subcommands and `history-backfill` (under the capture
  root); `index`, `ingest`, `repair-existing`, `upgrade`, `build-tools`,
  `add-this-mac`, `cloud-backup verify`, `repositories --merge --apply`,
  `refine-usage`, `repair-usage-attribution` and `findings --refresh`
  (beside the catalog, with a journal); `library-transfer` and
  `library-recover` (above). `TestEveryWritingCommandIsALeasedOperation`
  classifies every command. Not yet leased: adoption (`init-library
  --adopt`) and MCP servers.

### 6.6 The broker's registry

Broker operations (`library.transfer`, `library.recover`,
`library.deliver`, `library.create`, `service.start`) are app-level, not
library data. The broker keeps them in memory and in
`pharosSupportDir()/applications/<installation>/operations.json`, written
atomically (`durableJSON`) on start and end and at most every 10 s between:
running operations plus the last 50 ended ones. On broker start, running
entries become `interrupted`. The same `OperationSpec`, states and
vocabulary apply.

## 7. Snapshot

### 7.1 Service: one library

`GET /api/status` returns the document the surfaces render, and `GET
/api/status/stream` is a server-sent-events stream of it (phase 2). `GET
/api/operations` (phase 1) returns the same document with the journal's
recent rows and `freshness: []`; it has no stream. `GET /api/status?fresh=1`
(like `/api/operations?fresh=1`) runs the release path's full lock check.

```json
{
  "version": 1,
  "revision": 1842,
  "generated_at": "2026-10-27T16:02:11Z",
  "host": {"id": "…", "label": "Grady's MacBook"},
  "library": {"id": "…", "name": "Euclid", "drive": {"name": "Euclid", "ejectable": true}},
  "eject": {
    "verdict": "safe-in",
    "eta_seconds": 60,
    "summary": "Safe to eject in about 1 min: indexing stops after the current workspace.",
    "blockers": [{"operation": "op-1", "label": "Index changes", "on_eject": "…", "latency": "batch"}]
  },
  "operations": [{
    "id": "op-1", "kind": "index.changes.manual", "label": "Index changes", "verb": "Indexing changes",
    "state": "running", "state_label": "Running", "reason": null,
    "trigger": "user", "process": "service", "priority": "foreground",
    "started_at": "…", "ended_at": null,
    "step": {"index": 3, "count": 6, "name": "Claude Code", "unit": "byte", "done": 812000000, "total": 1980000000, "fraction": 0.41},
    "time": {"state": "estimating", "low_seconds": 180, "high_seconds": 360, "label": "about 3–6 min"},
    "stoppable": true, "stop": {"method": "POST", "path": "/api/operations/stop", "body": {"id": "op-1"}},
    "holds": [{"resource": "drive", "mode": "write"}], "affects": ["conv:claude", "view"],
    "children": [], "children_count": 2,
    "severity": "none", "legacy": {"activity_id": "…", "run_id": "…"}
  }],
  "attention": [{"id": "backup-failed", "severity": "error", "title": "The last backup failed",
                 "detail": "…", "action": {"label": "Open Backups", "path": "/settings/backups"}}],
  "freshness": [{"domain": "an.tools", "group": "analyses", "label": "Tools rollup", "as_of": "…",
                 "behind": {"state": "behind", "count": null, "unit": null}, "state_label": "Behind",
                 "reason": {"code": "deferred-by-schedule", "text": "Scheduled indexing skips analyses"},
                 "catching_up": null, "catching_up_label": null, "next": "updates with the next manual index"}],
  "freshness_summary": {"all_current": false, "checked_at": null, "groups": [{"group": "analyses", "label": "Analyses", "state": "behind",
                         "short": "waits for manual index", "text": "behind, updates with the next manual index", "as_of": "…", "shown": true}]},
  "resources": {"cpu_percent": 3, "memory_bytes": 340000000, "label": "Pharos is using ~3% of one core · 340 MB", "…": "§10"},
  "headings": {"work": "Work", "data": "Data"},
  "journal": {"dropped": 0}
}
```

- `operations` holds this host's top-level operations that are not
  terminal, plus those that ended in the last 10 minutes, newest first.
  `children` holds running children; `children_count` all of them. Daemons
  and kinds marked hidden (`drive.measure`, `maintenance.origins`,
  `maintenance.mcp-history`, `ask.save`, `cloud.reconcile`,
  `maintenance.cache-warm`) are omitted but still count for the verdict.
- A tentative daemon pass (§3.4) is not listed and not journaled; it
  counts for the verdict only. The ETag covers the listed operations, the
  whole `eject` document and the journal rows.
- `step.fraction` is present only when `unit` is real and `total` known.
  There is no overall percentage.
- `time` reuses the forecast states of
  [Progress and time estimates](progress-estimates.md). A range is shown
  only for `estimating`, or `provisional` marked as early ("about 3–6 min,
  early estimate"); otherwise `label` is null and surfaces show only "step
  N of M".
- `freshness` (the §5 rows) and `freshness_summary` are filled from phase
  3; `resources` (§10) from phase 4, `null` when nothing could be read.
  `headings` (`{"work": "Work", "data": "Data"}`) are the words over Show
  all's two parts.
  `/api/operations` keeps `freshness: []`. `attention` (§7.3) is filled
  from phase 2.
- Phase 2 adds to `/api/status`: `library.drive.location` and `mounted`,
  `library.portable` and `library.path`, and `features.unified_activity`
  (the flag, §9). It drops `journal.recent`, keeping `journal.dropped`.
- `eject.headline` (both documents, phase 2 round 1): the verdict as one
  short clause ("Safe to eject in ~2 s: stops back up first"); `summary`
  stays the full sentence, including the release's 7 s limit, for
  tooltips.
- Phase 1 also returns, per operation, `variant`, `subject`, `detail` (the
  state with its detail, §8.2), `on_eject` and `latency`, and `step.source`
  where a step is per source; and `journal.recent`, this Mac's newest 50
  journal rows. An operation of another process lists `process`
  (`cli:<command>`) and is never stoppable.
- **Words come from the server.** `label`, `verb`, `state_label`,
  `eject.summary`, attention `title`/`detail`/`action.label` and
  `time.label` are server text. Clients format numbers, not sentences.

**Stream.** `text/event-stream`; event `snapshot` carries the whole
document. Sent on connect, then on change, coalesced to at most one per
500 ms, with a `: keepalive` comment every 15 s. A stream also recomputes
every 2 s without a registry change (leases, attention and lock evidence
change without one) and sends only a document whose content changed.
**One computation:** every reader of a service (each stream and each GET)
shares one cached document, computed at most once per 500 ms, outside the
hub's lock and one at a time, in the background on the service's lifetime
with a 5 s limit. A reader that has a cached document waits at most 1.5 s
(below the broker's 2 s fetch timeout) and then gets the cached one; a
reader whose request ends stops waiting. The 500 ms window counts from when
the last document was published. The cost does not grow with
the number of pages. Change detection ignores every `generated_at`,
nested ones included.
**Polling fallback:** `GET /api/status` with `If-None-Match: <ETag>` returns
304 when unchanged (the store's tag is the last document it showed, from
a poll or a stream event's `id`). A refresh after an action (Stop) is one
fetch that schedules nothing while the stream is open. The client store
polls every 5 s once the stream fails,
tries the stream again after 30 s of polling, and holds neither a stream
nor a poll while the page is hidden.

### 7.2 Broker: every library

`GET /api/app/status` and `GET /api/app/status/stream` (phase 2), and the
phase-1 `GET /api/app/operations` (`?libraries=0` returns only `app`, asking no library's service: the native
menu's poll, which runs on its own schedule, apart from `/api/app/state`):

```json
{
  "version": 1, "revision": 77, "generated_at": "…",
  "libraries": [{
    "id": "…", "name": "Euclid", "selected": true, "connected": true,
    "service": {"state": "running | starting | stopped | crashed", "pid": 4120, "started_at": "…", "restarts_10m": 0},
    "snapshot": {"…": "the service document above, or null when not connected"},
    "eject": {"verdict": "…", "summary": "…", "blockers": []},
    "error": null
  }],
  "app": {"operations": ["broker operations, same shape"], "update": {"phase": "…", "progress": 42}},
  "attention": ["the union over libraries plus app-level items"],
  "resources": {"…": "the machine's total over every Pharos process on this Mac (§10); each library entry has its own"}
}
```

`/api/app/status` reads each connected child's `/api/status` (which the
child serves from its shared document) and recomputes each library's
`eject` with its own operations (§4.4); `/api/app/operations` reads
`/api/operations`. The broker's stream asks again every second, and on
any change to its own registry, rather than holding a stream to each
child; its readers share one document as a service's do. In the app the
page reads the broker's stream; the reverse proxy passes a child's
`/api/status/stream` through unbuffered. Neither endpoint takes the
broker's main lock. `GET /api/app/status?fresh=1` asks only the selected
library's service for `/api/status?fresh=1`, at that moment, with an 8 s
timeout inside the check's 10 s, and bypasses the shared document; the panel's Eject in the app acts only on it, for the selected
library, and refuses when it is missing or not fresh. Each library's `eject` is the
worse of its service's verdict and the broker's: every running broker
operation that touches the library (other than the release itself) is a
`busy` blocker, whatever its holds. Broker and lease operations name
libraries by registered ID in `libraries` (a lease's configuration paths
are in `library_configs`). Each library entry has a `state_label` (the verb of a broker operation touching it, else
Open, Opening or Not open). The web panel shows running `app.operations`
that touch the selected library on its Now row, and the rest under Other.
It never opens a catalog
to build this document; today's `/api/app/state` does (taxonomy D6), and is
left as it is.

### 7.3 One attention rule set

Computed once, on the server, for the snapshot, the native menu and the
web chip. It is the union of today's native rules (`menuBarStatus`,
`menu_bar.go`) and web rules (`problems()`, `library.js`), minus the false
positives fixed in phase 0:

| # | Rule | Severity | Action |
|---|---|---|---|
| 1 | The latest attempt for an enabled source on this Mac (across `source_states` and `automatic_source_states`) has an error other than `interrupted:` | error | Open Capture & index |
| 2 | Index verification has open mismatches (`sync_integrity_issues`, state `open`) | error | Review… |
| 3 | The latest finished index on this Mac failed, was not stopped by the user, and covered an enabled source (or failed before choosing sources). A run that only deferred sources to a capture is not a failure. | error | Open Capture & index |
| 4 | The latest finished index was interrupted (not by the user) and nothing has run since | warning | Open Capture & index |
| 5 | The latest capture failed for an enabled source, or failed as a whole | error | Open Capture & index |
| 6 | The latest local backup failed | error | Open Backups (`/settings/backups`) |
| 7 | A scheduled cloud backup failed (the schedule has `failures > 0`) or the latest verification failed | error | Open Backups |
| 8 | The library is unavailable, was disconnected, or setup is needed (native rules today) | error | as today |
| 9 | The WAL is over 1 GiB and its checkpoint has stopped advancing: frames are left to copy and `nBackfill` did not move between two busy bound checks (a busy truncation alone, with every frame copied, is not a stall). The item says how big the log is and for how long it has not been folded in, and who holds them back when Pharos can tell (another process by pid and command, and what it holds; or this process, with its running operations that started before the stall); otherwise it says it could not tell. It clears once a checkpoint succeeds, the checkpoint advances, or the log is under 1 GiB. | warning | Open Capture & index |

Rule 4 is today's web rule (the native menu ignores interruptions). The
first rule that applies sets the menu's single line; the snapshot lists all.

Phase 2 (`status_attention.go`): rules 1–7 and 9 are the service's, each item
`{id, rule, severity, title, detail, action: {label, path}}`; rules 3 and 4
are left out while an index or capture runs, as the web rule does, and
read the journal's latest ended top-level index row as well as this
process's runs (the newer wins), so a restart or crash keeps the warning.
The journal row applies the same filters: rule 3 only for a failure whose
recorded sources (`detail_json.sources`) include an enabled one, or that
has none recorded; rule 4 not for an interruption with reason `released`
(the service was released on purpose: a release before an eject, a quit, a
switch; `library.release` trigger `user` or `signal`), which is the user's
doing. It still reports `owner-exited` (a crash) and `service-stopped` (the
service stopped unasked: its parent app exited, its listener failed).
Rule 8
is split: the broker adds "<library> is not answering" (its service did not
answer) and "<library> is not open" (selected, no service, not starting),
each naming its `library`, only after two documents in a row and never
while a broker operation touches the library or its service is starting;
the native app keeps its own disconnection and
setup rules, which come before the snapshot's line. The broker's
`attention` is the union of every library's items, each with its
`library`, plus its rule 8 items. A query that fails leaves its rule out.

## 8. Vocabulary

### 8.1 Index, not sync

User-facing text says **index**. "Sync" stays only in internal names:
`SyncRun`, `/api/sync/*`, the `sync_*` tables and `sync.js`. The internal
run kinds map to labels:

| Internal | Kind | User label |
|---|---|---|
| `automatic-sync` | `index.changes.automatic` | Index changes (automatic) |
| `manual-sync` | `index.changes.manual` | Index changes |
| `capture-index` | `index.captures` | Index captures |
| `library-update:update` | `library.update` | Capture & index |
| `library-update:full` | `library.rebuild` `full` | Full recapture & reindex |
| `library-update:retained` | `library.rebuild` `retained` | Reindex from stored messages |
| `verification` | `index.verify` `all` | Verify retained inputs |
| `source-sync` | `index.sources.legacy` | Index sources |
| chain phase `github` … `findings` | `analysis.*` | the labels in §3.3 |

"Delivery" is only the broker's capture-then-move (`library.deliver`).
"Library view" is called the **Search view** in user text (the page is
Search).

### 8.2 State words

| State | Word | With detail |
|---|---|---|
| `queued` | Waiting to start | "Waiting to start: after Index changes" |
| `running` | the kind's verb | "Indexing changes · step 3 of 6 · Claude Code" |
| `blocked` | Waiting | "Waiting for the capture on Grady's MacBook" |
| `stopping` | Stopping… | "Stopping after the current workspace" |
| `succeeded` | Done | "Done 2 min ago" |
| `failed` | Failed | "Failed: <error>" |
| `cancelled` | Stopped | "Stopped by you", then the kind row's stop text ("the next index resumes it"; "start it again to upload from the beginning") |
| `interrupted` | Interrupted | "Interrupted", then the kind row's stop text |
| `skipped` | Skipped | "Skipped: already running" |

Never: "deferred", "Working" for an idle library, "Automatic sync in
progress" for work that is not an automatic index, "for this stage" for a
whole-operation estimate.

Freshness states (§5), as `state_label`: `current` Current, `scheduled`
Scheduled (muted: "updates tomorrow"; never "stale"), `behind` Behind,
`unknown` Unknown. The Data row's short words: "updating", "behind <age>",
"waits for manual index", "unknown", "failed", "updates <when>".

### 8.3 Reason codes

Blocked: `capture-running`, `ingest-busy` (another run holds the
coordinator), `catalog-busy` (the writer, with retries), `destination-busy`,
`waiting-for-drive`, `waiting-for-network`. Skipped: `already-running`,
`nothing-to-do`, `disabled`, `capture-busy`, `deferred-to-<kind>`,
`stopping`. Interrupted: `owner-exited` (its process died), `released`
(an intended release, eject, quit or switch), `service-stopped` (the
service stopped unasked: parent exited, listener error). Each code has
one server-side sentence.

## 9. Feature flag and rollout

`pharos-unified-activity-v1` is a boolean in the catalog's `ui_preferences`,
default `false`, toggled in Settings → Preferences (phase 2 adds it).

**Gated by the flag:** every new surface (the rebuilt drive panel, the
native menu rendered from the snapshot, Settings pages that read the
snapshot or ledger) and every behaviour change (phase 4's IO priority, the
registry refusing to start drive-holding work during a release).

Phase 2 wiring: the switch is "Unified activity panel (preview)" in
Settings → Preferences, saved at once. Go reads the flag through
`Catalog.unifiedActivity` (`feature_flags.go`), cached for 5 s and cleared
whenever preferences are saved. The page reads it from `pharosPrefs`
(`status-store.js`); with it on, `status-panel.js` is the one renderer of
the header chip, its panel and Index changes' state, and `library.js` and
`sync.js` leave those alone; turning it off gives them back without a
reload. `/api/menu-bar/status` carries `unified_activity`, and only when it
is true does the native menu also poll the snapshot. The panel's Eject
requests `/api/status?fresh=1` and acts only on that result; in the app it
requests `/api/app/status?fresh=1` instead and acts only on the broker's
fresh verdict for the selected library (§7.2), refusing when that is
missing, names another library or is not `checked: fresh`; it never acts
on the library's own verdict there. `safe` ejects, `safe-in` asks first
and checks again, `busy` and `unsafe` refuse.

**Runs regardless, in shadow:** the registry, the journal, the broker's
registry, the snapshot endpoints and streams, the freshness function, and
resource sampling. A test compares the shadow snapshot with
`libraryActivities` during runs: every activity has an operation; every
operation that holds the drive and is in scope of `libraryActivities` has
an activity; and the verdict is never `safe` while `libraryActivities`
lists a writer (§4.3).

**Dual-write, no retirement.** Existing endpoints, fields, pollers and the
old drive panel keep working unchanged. Removing them is a later phase,
not part of this series.

**Unregistered work is a test failure.** A Go test parses the package's
non-test files and fails on any `spawn(`, `runBackground(` or `go func`
that is not inside `start`/`begin`, not a registered daemon, and not on an
allowlist with a reason (request handling, subprocess pipes, timers).

## 10. Resources

Phase 4 measures, and the snapshots report, per process (`resources.go`):

- **Which processes.** A service reports itself and the other Pharos
  processes on this Mac holding a live lease on its library (§6.5; the
  `pid` is read from the lease file while its flock is held, and a lease
  whose `host_id` is another Mac's is skipped). The broker reads itself,
  each library's service by the PID it started (so a service too busy to
  answer is still measured), the Terminal commands each service's snapshot
  names (or, without a service, the library's leases), and the app-level
  commands' leases. The broker's own figure leaves out its exited
  children's time: they are the services it reads itself, and a service
  reaped after hours would otherwise count its whole life again. The
  native app (Swift) is not measured.
- **How.** `proc_pid_rusage(pid, RUSAGE_INFO_V4)`, made as the `proc_info`
  system call (336, `PROC_INFO_CALL_PIDRUSAGE`) with `syscall.Syscall6`,
  as `capture_iopolicy_darwin.go` makes `iopolicysys`: the package has no
  cgo, and `macos/build-app.sh` builds with whatever `go env CGO_ENABLED`
  says, so the release build must not need it. Times are Mach units. The
  rate is measured once against this process's own `getrusage` (snapped
  to 1 GHz or 24 MHz within 5 %), not taken from `hw.tbfrequency`, which a
  process under Rosetta may be told differently; until the process has
  used 20 ms of CPU, `hw.tbfrequency` stands in. No `ps` fork. Off macOS a process reads
  only itself (getrusage and the Go runtime's memory), with `basis`
  `self-reported` and `approximate: true`.
- **When.** Only when a snapshot is computed, and at most once per process
  per 1.5 s (`resourceSampleInterval`): Pharos measures only while someone
  is looking. Nothing is sampled while no page is open, and with any
  number open each process is read at most every 1.5 s (in practice every
  2 s, the stream's recheck). This replaces the fixed 2–5 s loop first
  planned; the sampler starts no goroutine. Measured cost: about 20 µs of CPU for a
  sample of ten processes and its summary, 0.0013 % of one core
  (`TestResourceSamplingIsCheap`).
- **What.** CPU is the delta of own plus exited children's user and system
  time over the wall time between two readings, as % of one core (`null`
  until a second reading); memory is `ri_phys_footprint` (Activity
  Monitor's figure). A figure, and a total, never exceeds `NumCPU × 100`. A reading is
  a new baseline, with `cpu_percent: null`, when its PID was reused
  (another `ri_proc_start_abstime`), when the previous reading is more
  than 10 s old (no page was open: an average over the gap is not the
  current load), and when its children's time grew by more than the
  interval × cores (a long child reaped). A total's `cpu_percent` is
  `null` (memory only) while any of its processes has none. `cpu_percent`
  is a whole number and `memory_bytes` two significant figures.
- **Change detection.** `resources` objects are left out of the snapshot's
  tag (`statusTag`), so a document whose only change is its load keeps its
  ETag: a poll gets 304 and sees new resources only with another change. A
  stream carries the latest resources in every event, and sends a
  resources-only update at most every 5 s (`statusResourcesRefresh`)
  while a client listens.
- **Subprocesses** (`git`, `gh`, `diskutil`, CLIs) count only when they
  exit and are reaped, in the child time of the process that ran them: a
  short call shows in the interval it ended, and one that ran longer than
  the interval could account for starts a new baseline instead.
- **Per-operation** attribution inside a process (pprof labels, already
  used by findings detectors) is not built; questions about load need only
  per-machine and per-library answers.
- **Index runs' telemetry** (`incremental_metrics.go`) reads its RSS from
  the same call's `ri_resident_size`, the figure `ps -o rss` reports, so
  RSS history stays comparable; `ps` remains the fallback. Recorded CPU
  shifts a little lower: it includes `RUSAGE_CHILDREN`, which counted the
  once-a-second `ps` forks. This is not behind the flag: it changes no
  behaviour and drops one fork per second of a run.

Shape, in `/api/status` (`resources`, `null` when nothing could be read)
and in `/api/app/status` (the machine's total as `resources`, and each
library entry's own as `libraries[].resources`):

```json
{"cpu_percent": 45, "memory_bytes": 1200000000, "sampled_at": "…", "basis": "proc_pid_rusage", "approximate": false,
 "label": "Pharos is using ~45% of one core · 1.2 GB",
 "processes": [{"pid": 4120, "role": "service", "library": "lib-a", "cpu_percent": 38, "memory_bytes": 1100000000,
                "sampled_at": "…", "basis": "proc_pid_rusage", "text": "Library service (Euclid) · ~38% of one core · 1.1 GB"}]}
```

`role` is `service`, `broker` or the lease's process (`cli:ingest`).
`label` and `text` are the server's words; the panel's Load row shows the
machine's `label` in the app and the library's otherwise, with the
processes' `text` in its tooltip.

**Priority classes to IO policy** (applied only with the flag on):

| Class | Disk IO policy | Applied |
|---|---|---|
| `foreground` | default (`IOPOL_DEFAULT`) | — |
| `background` | utility (`IOPOL_UTILITY`), per thread | `Op.RunIO` around the operation's IO-heavy sections, with `runtime.LockOSThread`, as `withCaptureIOPolicy` does |

The class is applied in one place, the handle: `Op.RunIO(fn)` runs fn
under utility when the operation's priority is `background` and the
registry's `lowerIO` says so; otherwise it runs fn unchanged. `lowerIO`
reads the flag from memory only (`Catalog.unifiedActivityNow`): saving
preferences sets it, every read of the flag refreshes it, and the
service's `watchFeatureFlags` daemon reads it every 5 s, so work never
waits on a reader connection for it. The thread returns to Go's pool only
once its policy is restored; if restoring fails, the goroutine keeps the
thread locked and Go ends it rather than reuse it at utility. The policy
is per thread and goroutines fn starts do not inherit it, so it wraps the
loops that do their IO on their own goroutine:

- every operation started with `start`, `launch` or `startBackground`: the
  whole body (`runOperation`), so the Git backfill and refresh, the
  upgrade count, `drive.measure`, and the writes requests hand to the
  background (`maintenance.mcp-history`, `cloud.reconcile`, `ask.save`);
- the automatic index (`index.changes.automatic`): each source's discovery
  walk, each source's ingest, the Search view projection and the audit;
- each stage of the analysis chain, which takes its run's class (a child
  with no priority of its own inherits its parent's): foreground under a
  manual index, background under `analysis.refresh`;
- each batch of a daemon pass (search index builds, the Search view,
  previews, parallelism, cache warming, the origins publication, the cloud
  schedule tick's `cloud.reconcile`), not the pauses between them;
- the WAL bound's checkpoint (`maintenance.checkpoint`), begun as a child
  of the caller's operation and so in its class: a manual index, a capture
  or a release keeps the default tier; a caller with no operation (a
  close, a repair) is foreground; only `keepWALSmall`'s ticker runs it as
  a background root. Only its passive pass (the bulk copy, which takes no
  writer lock) follows the class; the truncating pass, which holds
  writers off, always runs at the default tier;
- the broker's delivery (`library.deliver`): its pending capture and its
  move. The broker has no catalog, so its `lowerIO` is the flag the
  selected library's service reports (`features.unified_activity`), read
  from every status document and asked for again before each delivery.
  The value belongs to the selected library: another selection clears it,
  and a selected library whose service cannot confirm it (none running,
  or no answer) is off.

Work run inline inside a background body inherits its utility thread,
whatever its own class: a Tools rollup that `analysis.refresh` rebuilds
while a Tools read waits runs at utility.

Not covered: work an operation hands to other goroutines (capture's hashing
pipeline, a backup's WAL poller), and Terminal commands, whose registries
have no `lowerIO`.
The three copy paths that ran at utility before (capture copies, local
backups, adoption) keep doing so with the flag on or off: they are not
moved to the default tier even where their kind is foreground. A
read-started rebuild that a request or page waits on is registered
foreground and keeps the default tier: `analysis.tools` from a read,
`analysis.findings` from Refresh findings and from `GET /api/findings`
before findings exist, and `analysis.authorship` from a page. A background body holding the catalog writer
at utility can make a foreground writer wait a little longer; the utility
tier, unlike background, is not throttled hard.

## Known hazards this contract must not hide

Found while writing it (phase 0 report has file and line):

- **Delivery stops the selected library's service** every 5 minutes while a
  delivery pair is set, whether or not anything is pending.
- **The broker's 2-minute start timeout** can SIGTERM a service still in its
  first `catalog.initialize` after an update, and the maintain loop then
  starts it again every 5 s.
- **Cloud verification** leaves each recovered copy in staging with nothing
  to remove it, and `/api/health` walks staging on every call.
- **`pharos ingest`** takes no ingest coordinator lock.
- **Capture this Mac** cannot be stopped, and holds the ingest coordinator
  for the whole capture.

## Parked investigations

These came out of measuring a real library (the author's euclid library on
Pharos 0.7.5, 2026-10-27). They are recorded for later and not scheduled. PRs
landing in the meantime may make some of them unnecessary, so check each one
against current code before starting it.

- **Forecast ranges are much wider than the evidence needs.**
  - Automatic runs: the point estimate's median relative error is about 6% at
    launch, yet the interval contains the outcome in about 99% of runs (75 of
    76), with a median high/low ratio of about 9.
  - Manual Index changes: median interval widths of 870–3,745 s on runs of
    4–6 min, with 14 of 14 covered.
  - The worst-case composition (adding the highs of sequential steps, the
    live ×2 and overrun ×3 widening) plus calibration targets far more
    coverage than needed. Aim for 80–90% (§10 and
    [progress estimates](progress-estimates.md)).
- **Stopped runs never train the forecast.** Capture & index runs took
  between 6 minutes and 5 hours, every snapshot was `partial`, and 3 of 5
  were interrupted. Interrupted runs record no outcome, so the operation
  people most want a time for never reaches a total. Learning from the steps
  that did finish would fix that.
- **Memory peaks around 9.6 GB during routine incremental runs.** That is the
  median of the observed peak RSS (p95 9.8 GB, max 11.2 GB), at a median CPU
  of 236% of one core. The figure is process-wide, so concurrent maintenance
  is included. A lead: the findings feature cache, which allocates about 7 GB
  on its cold daily rebuild.
- **The per-pass audit is almost always deferred.** 183 of the last 200
  incremental runs have class `changes_audit_deferred`. Find out whether the
  5 s / 16 MiB budget, writer contention or the capture lock is the cause.
- **Retire the old status paths.** This is the phase that follows this
  contract's rollout. Once the flag defaults on and has been stable, remove
  the run-record progress fields, the `detail` strings, client grouping by
  label, the duplicate pollers and the three older freshness rules (§5).
- **SQLite maintenance as operation kinds.** Add automatic `VACUUM`, a
  periodic `ANALYZE` refresh (statistics are currently built only when
  missing) and FTS `optimize`, each a registered, background-priority kind
  under §3.
