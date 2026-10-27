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
	Subject  string   // optional: source name, capture label, destination ID
	Parent   OpID     // zero for a top-level operation
	Trigger  Trigger  // schedule | user | chained | startup | version | read | device | cli | respawn
	Holds    []Hold   // what it holds while running (§4); defaults from the kind row
	Affects  []Domain // freshness domains it brings current (§5); defaults from the kind row
	Priority Class    // foreground | background; defaults from the kind row
	Journal  Policy   // always | if-longer-than(T) | never; from the kind row, not overridable
	Legacy   string   // ID of the record it shadows (SyncRun.ID, BackupRun.ID, cloud run ID…)
}

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
- The registry also feeds the existing models during dual-write: `Step` and
  `Advance` on an op with a `Legacy` run ID update that `SyncRun`'s `Work`
  exactly as `runWorkMeter.report` does today, so `libraryActivities` and
  the old drive panel keep working unchanged.

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
  release (`detail.during_release`).

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
| `capture.cli` | Capture (Terminal) · Capturing | cli | cli (lease only; it never opens the catalog) | D:w C(this):w | capt:this/\* | — | Pharos cannot stop it; the drive stays busy until it ends. | none (to Pharos) | never (lease only) | fg |
| `index.captures` | Index captures · Indexing captures | user, onboarding, cli | svc, cli | D:w W:w I:w X(c):w (`I` in the service only) | conv:\*, view, an.\*, findings | index | Stops between records; the next index resumes it. Waits for a running capture (`blocked(capture-running)`). | batch | always | fg |
| `library.update` | Capture & index · Capturing and indexing | user | svc | D:w W:w I:w | capt:this/\*, conv:\*, an.\*, findings, upgrade | index | As `index.captures`; the browser then starts `library.upgrade`. | batch | always | fg |
| `library.rebuild` (variants `full`, `retained`) | Full recapture & reindex / Reindex from stored messages · Rebuilding | user, resume | svc | D:w W:w I:w | conv:\*, view, an.\*, findings | index | Stops between workspaces; resuming the repair continues it (`sync_recovery_jobs`). | batch | always | fg |
| `index.verify` (variants `sample`, `all`) | Verify retained inputs · Verifying | user; `sample` is a step of index runs | svc | D:w W:w I:w (reads retained inputs; writes `sync_verifications` and integrity issues) | — | index | Stops between inputs. | record | always (`all`); step only (`sample`) | fg |
| `index.sources.legacy` | Index sources (legacy API) · Indexing | user (API) | svc | D:w W:w I:w | conv:\* | — | Runs inside the request, with the service's lifetime merged into its context: a release stops it between workspaces. | batch | always | fg |
| `ingest.cli` (variants `ingest`, `repair-existing`) | Index (Terminal) · Indexing | cli | cli | D:w W:w (no `I` today) | conv:\* | — | Pharos cannot stop it. | none (to Pharos) | always | fg |
| `host.add` | Add this Mac (Terminal) · Adding this Mac | cli | cli | D:w W:w | conv:\* | — | Pharos cannot stop it. | none (to Pharos) | always | fg |
| `capture.history-backfill` (variants by subcommand) | Capture history backfill (Terminal) · Backfilling history | cli | cli | D:w C(h):w | capt:h/\* | — | Pharos cannot stop it; its native-snapshot pause blocks ordinary capture of the chosen sources until released. | none (to Pharos) | always | fg |

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
| `maintenance.checkpoint` (variants `bound`, `after-run`, `close`) | Save the catalog · Saving the catalog | daemon pass (WAL over 64 MiB, every 10 s), chained, release | svc | D:w W:w | — | — | Not interruptible; short. | record | >1 s | bg |
| `maintenance.metrics` | Update MCP metrics · Updating metrics | none: requests never write, so a `query_statistics` call computes dirty workspaces' metrics in its read snapshot and `maintenance.library-view` stores them; the kind stays so that journal rows naming it still render | mcp, svc | D:w W:w | — | — | Ends with the call. | record | never | fg |
| `maintenance.origins` | Publish conversation origins · Publishing conversation origins | daemon pass (`maintainLibrary`: every pass while no ingest runs, at most once a minute during one); the end of each ingest and the Tools rollup publish them as part of their own operation | svc | D:w W:w | — | — | Stops; it publishes them on its next pass. | batch | never | bg |
| `maintenance.mcp-history` | Store MCP call history · Storing MCP call history | read (an MCP call through the service queues its record; one background writer stores the queue, so no request waits for the writer) | svc | D:w W:w | — | — | Stops; closing the catalog stores what is still queued. | record | never | bg |
| `ask.save` | Save an Ask Pharos answer · Saving an Ask Pharos answer | user (a finished Ask turn whose save missed its deadline is retried in the background for up to 5 min) | svc | D:w W:w | — | — | Stops retrying; an answer not yet saved is lost, as when the service stops. | record | never | bg |
| `drive.measure` | Check the drive · Checking the drive | read (`GET /api/health/drive`, 5 min cache) | svc (+`diskutil`) | D:w W:w (one `library_size_history` row) | — | — | Stops. | record | never | bg |

**Very frequent tiny work.** WAL bounding checks every 10 s; almost every
check does nothing. The check is the daemon. Only a check that runs a
checkpoint starts `maintenance.checkpoint`, which is registered (so eject
sees it) but journaled only past 1 s. The same pattern applies to every
`>T` row: register always, journal only past T or when `op.Wrote()` was
called. `drive.measure`, `maintenance.metrics`, `maintenance.origins`,
`maintenance.mcp-history`, `ask.save` and `cloud.reconcile` (§3.6) are
listed only so the unregistered-work test passes and eject sees them; they
are D-family and hidden from surfaces. Requests never write: the writes
they used to make (origins, MCP history, interrupted cloud runs) are these
background operations, and no request waits for one. Ask Pharos stays out of scope (§1): only the background
save of a finished answer, which writes the catalog after its request has
ended, is registered.

### 3.5 E. Transitions

| Kind | Label · verb | Trigger | Proc | Holds | Affects | Stop | On stop or eject | Latency | Journal | Pri |
|---|---|---|---|---|---|---|---|---|---|---|
| `catalog.initialize` | Prepare the catalog · Preparing the catalog | every writable catalog open (skipped when `meta.initialized_build` matches this executable) | svc, cli, mcp | D:w W:w Z:w | all derived domains | — | Cannot be stopped; the service has no API until it ends. A kill rolls back the step in progress, and the next open re-runs from the last committed `meta` key. | none | >2 s | fg |
| `library.upgrade` | Upgrade the library · Upgrading the library | chained (after an index finds pending steps, or the browser after Capture & index), user (`POST /api/upgrade`), cli | svc, cli | D:w W:w | upgrade, an.tools | — | Stops after the unit in progress; starting the upgrade again resumes it. | record | always | fg |
| `tools.ledger.build` | Build the Tools ledger · Building the Tools ledger | user (`POST /api/tools/backfill`), version, step of `library.upgrade` | svc, cli (`build-tools`) | D:w W:w | an.ledger, an.tools | — | Stops between conversations; building the ledger again resumes it. | record | always | fg |

Notes:

- **Version catch-ups** (E4) are the C kinds above with trigger `version`.
- **Initialize and the journal.** The journal table is created by
  Initialize's schema step, so `catalog.initialize` writes its row (start
  and end together) after that step; a kill before it leaves no row. The
  broker separately records `service.start` (G), which covers the wait.
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
| `cloud.reconcile` | Record interrupted cloud backups · Recording interrupted cloud backups | read (`GET /api/cloud-backup` that finds a run whose process is gone; the schedule tick reconciles inside its own pass) | svc | D:w W:w (and removes the run's staging) | — | — | Stops; the next Cloud page load or schedule tick records them. | record | never | bg |
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
| `library.release` | Release the library · Releasing the library | user (eject, quit, switch), device | svc, brk | D:w | — | — | Is the stop. Fails after 7 s with "Pharos is finishing a write". | — | always | fg |
| `app.update` | Update Pharos · Updating Pharos | schedule, user (Sparkle) | app | update-lock:w (native) | — | — | Native; bridged to the header ring as today. | — | never (Sparkle keeps its own) | bg |

Not kinds: the broker's 5 s maintain loop (a daemon whose respawns are
`service.start` with trigger `respawn`), the native 5 s state watch, the
service's watchdogs (parent gone, drive gone, SIGBUS), DiskArbitration
monitors, the quit gate (a consumer of the eject verdict), MCP stdio servers
themselves (only their `catalog.initialize`), and service log capture.

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
| `unsafe` | Unknown or unregistered writer evidence: kind `unknown`; unregistered running work found by the registry's self-check; a held capture or index lock (today's flock probes) that no held lease and no operation in this service's registry names; a lease file that cannot be read or probed; the service unreachable while the drive is mounted | Eject is refused; the panel says what is unknown |

So a Search view refresh alone (`maintenance.library-view`, a `record`
writer) is `safe-in`, never `safe`.

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

Phase 3 implements these as one function over today's storage; storage does
not change. Every row has:

- `as_of`: when the domain was last brought current (RFC 3339), or null if
  never;
- `behind`: `{"state": "current" | "behind" | "unknown", "count": N, "unit": "…"}`;
- `reason`: one code from the list below, or null when current;
- `catching_up`: the ID of the queued or running operation that will make it
  current, or null;
- `next`: what will catch it up when nothing is running ("next scheduled
  index", "next manual index", "next time Pharos opens the library").

| Domain | `as_of` from | Behind measured by | Reason codes | Caught up by |
|---|---|---|---|---|
| `conv:<source>` (this Mac's live sources) | max of `automatic_source_states.indexed_at`/`succeeded_at` and `source_states.last_success_at` for this host | `unknown` unless a run is discovering (its plan's changed units) | `indexing`, `never-indexed`, `source-error`, `capture-busy`, `needs-capture` (`pending_preservation`), `schedule-off`, `interrupted` | `index.changes.*`, `library.update`, `index.sources.legacy` |
| `capt:<host>/<source>` | capture manifest `last_run` | `needs_index` from `capturedHosts` | `not-indexed`, `indexing`, `capture-running` | `index.captures`, `library.update` |
| `view` (Search view rows) | last time `workspace_library_dirty` was empty | `COUNT(*)` of `workspace_library_dirty` (capped at 100,000) | `refreshing`, `stopped` (`libraryRefreshStopped`), `version` (`workspace_library_version`) | `maintenance.library-view` |
| `search.substring` | done flag `substring_index_ready` | `substringIndexProgress` done/total | `building`, `version` | `maintenance.search.substring` |
| `search.text` | `text_search_ready` | cursor, total unknown | `building`, `version` | `maintenance.search.text` |
| `search.tools` | `tool_command_fts_version` current | `tool_command_fts_cursor`, total unknown | `building`, `version` | `maintenance.search.tools` |
| `search.query` | `query_text_ready`, `tool_query_facts_ready`, `small_surface_index_ready` | unknown | `building`, `version` | `maintenance.search.query` |
| `an.identities` | `sync_deferred[identities].completed_at` | `generation − completed_generation` | `deferred-by-schedule` (scheduled indexing skips analyses), `running`, `failed` | `analysis.chain`, `analysis.identities` |
| `an.git` | `sync_deferred[git].completed_at` | as above; per-repository 6 h retry timers are in memory (lost on restart) | as above, plus `git-unavailable` | `analysis.git`, `analysis.chain` |
| `an.tools` (Tools rollup) | `tool_rollup_generation` meta | `COUNT(*)` of `tool_rollup_dirty` workspaces | `deferred-by-schedule`, `running`, `version` | `analysis.tools` |
| `an.authorship` | `authorship_generation` | `authorship_inputs_generation ≠ authorship_generation` (behind, count unknown) | `deferred-by-schedule`, `running`, `never-built` | `analysis.authorship` |
| `an.ledger` (tool ledger) | `tool_ledger_generation` | conversations with no or an old `tool_ledger_state.version` | `version`, `running` | `tools.ledger.build`, `library.upgrade` |
| `findings` | `findings_built_at` | `findings_generation` ≠ `findingsVersion/<today>` means the daily full pass is due; `sync_deferred[findings]` | `daily-pass-due`, `deferred-by-schedule`, `running`, `never-built` | `analysis.findings`, `analysis.chain` |
| `upgrade` | — | sum of `UpgradeSteps` pending counts | `upgrade-available`, `running` | `library.upgrade` |
| `backup.local` | latest non-partial backup in `backups.json` | age only | `never`, `failed`, `running`, `destination-missing` | `backup.local` |
| `backup.cloud:<destination>` | latest `cloud_backup_runs` row that is complete and verified | age; schedule `next_run_at` | `never`, `failed`, `unverified`, `paused`, `running`, `retrying` | `cloud.upload`, `cloud.verify` |

Rules:

- `reason` is the **first** applicable code in each row's list order. Lists
  put running work first, so `indexing` (a run is on that source now) beats
  `source-error` from an earlier attempt.
- The three Go freshness rules disagree today: `Catalog.Freshness`
  (`catalog.go`) ignores incremental runs, MCP `archiveSourceStatus`
  (`mcp_audit.go`) includes them, and the menu's `indexed_at`
  (`menu_bar.go`) is the last write. The ledger uses the `conv:<source>`
  rule above for all three. Phase 3 replaces them.
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
  work. The writer runs a round at most every 3 s: it tries the gate without
  waiting (a non-blocking send on the gate channel); if the gate is taken,
  or the process has a write transaction in flight, it skips the round and
  keeps the pending images for the next one. When it has the gate it writes
  every pending image in one short transaction (`BEGIN IMMEDIATE` with a
  `busy_timeout` of at most 50 ms, so another process holding SQLite's lock
  also makes it skip), then releases the gate. It never retries while
  holding the gate.
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
  servers while they run `catalog.initialize`)
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
- **Lease lifetime.** A process takes its lease before it queues its first
  journal row, and keeps it until its last row is written: at exit it runs a
  final journal round with a bounded budget (1 s, through the same
  non-blocking rule plus one blocking attempt within that budget), and only
  then unlinks the lease (while holding its lock) and closes it.
- **When the final round cannot write.** The process writes its terminal
  outcomes into its lease file in place (`ftruncate` + `pwrite`, `fsync`;
  it still holds the lock): an `outcomes` map of op ID to state, reason,
  error and `ended_at`. Then it releases the lock **without unlinking** the
  file, and exits. The outcome survives in the unheld lease.
- **Reconciling in the background.** A reconcile pass runs on its own timer
  (every 10 s), independent of the writer gate and of the journal writer's
  rounds. For each unheld lease file (probed with `LOCK_EX|LOCK_NB`, which
  it keeps while it works on that file) it first reads any `outcomes` and
  queues each as a conditional reconcile (§6.2) with that recorded state;
  once those have been written, it deletes the file (the protocol above).
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
- **Fallback:** today's flock probes (`captureActive`) stay. A capture or
  index lock held with no live lease and no operation in this service's
  registry naming it makes the verdict `unsafe` (an older build's CLI, or a
  crash between taking the lock and writing the lease).
- `cloud-restore` to a new location is out of status scope (§3.6).

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

`GET /api/operations` returns the document; `GET /api/operations/stream` is
a server-sent-events stream of the same document.

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
  "freshness": [],
  "resources": null,
  "journal": {"dropped": 0}
}
```

- `operations` holds this host's top-level operations that are not
  terminal, plus those that ended in the last 10 minutes, newest first.
  `children` holds running children; `children_count` all of them. Daemons
  and kinds marked hidden (`drive.measure`, `maintenance.metrics`,
  `maintenance.origins`, `maintenance.mcp-history`, `ask.save`,
  `cloud.reconcile`, `maintenance.cache-warm`) are omitted but still count for the verdict.
- `step.fraction` is present only when `unit` is real and `total` known.
  There is no overall percentage.
- `time` reuses the forecast states of
  [Progress and time estimates](progress-estimates.md). A range is shown
  only for `estimating`, or `provisional` marked as early ("about 3–6 min,
  early estimate"); otherwise `label` is null and surfaces show only "step
  N of M".
- `freshness` is filled in phase 3 and `resources` in phase 4; until then
  they are `[]` and `null`.
- **Words come from the server.** `label`, `verb`, `state_label`,
  `eject.summary`, attention `title`/`detail`/`action.label` and
  `time.label` are server text. Clients format numbers, not sentences.

**Stream.** `text/event-stream`; event `snapshot` carries the whole
document. Sent on connect, then on change, coalesced to at most one per
500 ms, with a `: keepalive` comment every 15 s. **Polling fallback:** `GET
/api/operations` with `If-None-Match: "<revision>"` returns 304 when
unchanged; poll every 2 s while any operation is running and 10 s when
idle; stop while the page is hidden.

### 7.2 Broker: every library

`GET /api/app/operations` and `GET /api/app/operations/stream`:

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
  "attention": ["the union over libraries plus app-level items"]
}
```

The broker subscribes to each connected child's stream and recomputes each
library's `eject` with its own operations (§4.4). It never opens a catalog
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

Rule 4 is today's web rule (the native menu ignores interruptions). The
first rule that applies sets the menu's single line; the snapshot lists all.

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

### 8.3 Reason codes

Blocked: `capture-running`, `ingest-busy` (another run holds the
coordinator), `catalog-busy` (the writer, with retries), `destination-busy`,
`waiting-for-drive`, `waiting-for-network`. Skipped: `already-running`,
`nothing-to-do`, `disabled`, `capture-busy`, `deferred-to-<kind>`,
`stopping`. Each code has one server-side sentence.

## 9. Feature flag and rollout

`pharos-unified-activity-v1` is a boolean in the catalog's `ui_preferences`,
default `false`, toggled in Settings → Preferences (phase 2 adds it).

**Gated by the flag:** every new surface (the rebuilt drive panel, the
native menu rendered from the snapshot, Settings pages that read the
snapshot or ledger) and every behaviour change (phase 4's IO priority, the
registry refusing to start drive-holding work during a release).

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

Phase 4 measures, and the snapshot reports, per process:

- **CPU and memory** for the broker, each library service and the other
  Pharos processes found through live leases (§6.5; their `pid`, read from
  the lease file while its flock is held), whether or not they journal. The broker samples
  `proc_pid_rusage` (`RUSAGE_INFO_V4`) every 5 s: CPU is the delta of
  `ri_user_time + ri_system_time` over wall time (as % of one core);
  memory is `ri_phys_footprint`. No `ps` fork; the 1 s `ps` sampler in
  `incremental_metrics.go` is replaced behind the flag.
- **Subprocesses** (`git`, `gh`, `diskutil`) are counted when they exit, from
  `RUSAGE_CHILDREN` deltas, against the process that ran them.
- **Per-operation** attribution inside a process (pprof labels, already
  used by findings detectors) is later; questions about load need only
  per-machine and per-library answers.

**Priority classes to IO policy** (applied only with the flag on):

| Class | Disk IO policy | Applied |
|---|---|---|
| `foreground` | default (`IOPOL_DEFAULT`) | — |
| `background` | utility (`IOPOL_UTILITY`), per thread | around each operation's IO-heavy section, with `runtime.LockOSThread` as `withCaptureIOPolicy` does today |

Today only capture copies, local backups and adoption run at utility.
Phase 4 applies the class in one place (the handle), so ingest, acquisition,
cloud, maintenance and checkpoints follow their kind's priority instead of
three copy paths deciding for themselves. A read-started rebuild that a
user is waiting on is foreground.

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
