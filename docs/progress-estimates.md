# Progress and time estimates

The drive panel shows each active job and its current stage, source, elapsed
time, processed workload, and known follow-up work. A bar measures the named
stage. It does not promise that the entire library update is that far through.
The panel also shows a whole-operation forecast for incremental indexing,
Capture & index, full rebuilds, retained repairs, and explicit capture indexing.
Concurrent independent maintenance retains its own bar and estimate.

Capture measures bytes copied for the current source's file-copy stage.
Database snapshots and saving the manifest are separate stages. Indexing a
capture counts the changed conversation groups before parsing them (one group
can include a parent session and its subagents). That same discovery pass also
collects transcript sizes. Where possible, the bar and ETA use bytes for files
or message counts for database sessions, so a few large conversations carry
more weight than many small ones. Mixed or unavailable size signals fall back
to group counts. The panel shows both group counts and input volume when known.
Counts advance only after a record is successfully handled.

Incremental runs discover all selected sources before parsing any transcripts.
Incremental and capture indexing retain a metadata-only plan of conversation
groups and their input sizes, then execute that inventory. Claude/Codex do not
walk the tree again to parse; Conductor reads each planned session in a bounded
snapshot and records the version actually read. Antigravity retains group and
dependency metadata while preserving retries for moving or unreadable groups.
New groups appearing during live ingestion wait for the next run. Captured
plans are bound to their manifest and are rebuilt, with revised denominators,
if that capture changes before execution. Input sizes can be revised when a
planned input changes before it is read. Known empty inventories are distinct from
incomplete discovery. Shared database dependency sizes do not stand in for
transcript bytes.

Git refresh groups workspace sightings and worktrees by their shared Git
directory. It reads the remote-tracking default-branch tip once per repository,
batches object availability checks, shares first-parent history and per-head
answers, and saves results in bounded transactions. Unchanged, conclusive
negative results skip ancestry work. After a fast-forward, one newly reachable
commit set updates those negatives without checking each head again. Force
pushes fall back to fresh ancestry checks. Missing objects and shallow-history
negatives remain retryable. These checks use local Git objects and refs; they
do not fetch or pull repositories. Coordinated ingestion defers inline
Conductor ancestry lookups to this repository-wide refresh.

Git, identity linking, projections, authorship, tools, retained snapshots, and
findings report their own work units where available. SQL preparation, input
loading, and other work without a complete denominator still report an unknown
total. They do not inherit the preceding stage's progress. Their enclosing
operation steps record elapsed duration even without a measurable denominator,
so an opaque SQL stage does not erase an otherwise usable operation forecast.

Stage estimates are ranges with the basis visible below the bar. These
throughput bounds are separate from the whole-operation model:

- Live estimates need at least eight seconds and three sampled observations
  with advancing counts. Incremental workers supply observations even when
  no status panel is open. They use about a minute of recent work, including
  time spent on the current item. Variable speeds widen the range.
- Before there is enough live data, measured capture-copy, capture-index,
  incremental-index, and analysis stages can use the median throughput of at least three comparable successful
  stages on this Mac. History retains the last twenty samples and separates
  job type, provider, indexer version, rebuild versus update, stage, and
  measurement unit. Analysis stages share history across manual, capture,
  and background entry points. Samples outside one quarter to four times the current workload are excluded.
- Once live speed is measurable it gets at least 75% of the weight, with the
  historical rate bounded to half to twice that speed. History loses its
  influence over the first thirty seconds of observed progress.
  Legacy whole-run durations do not supply these stage throughput estimates,
  because they did not record how much work was performed.
- A new job, source, stage, measurement unit, changed total, or reset counter
  restarts live measurement. Work completed before observation or resumption
  is not credited to the new sample window. Restarted worker stages train
  throughput only from the counters advanced since that segment began.
- After at least 45 seconds with no completed work (longer for slow counted
  units), the countdown is withdrawn and the panel explains that the current
  item may be large or waiting. Fresh progress allows estimation again.
- A stop request replaces the estimate with a safe-stopping status. A failed
  status request hides estimates until the service can be reached again.

Incremental run history also retains individual stage durations, measured units,
completion state, observed conversation counts, planned conversation-group and
file counts, input bytes or database
message counts, time since the previous index, and one-minute machine load when
available. Transcript bytes are file lengths; database byte measurements
are the raw/decoded message-text bytes actually read, labeled separately.
Settings → Capture & index → run Details exposes these measurements.
Interrupted stages remain in run history without becoming completed ETA
samples. Workload features also feed the operation model described below.

Completing a measured stage does not imply the job is done: identity linking,
retained snapshots, flushing, and maintenance can still follow. The UI has no
fixed capture/index/upgrade percentages, elapsed-time animation toward 100%,
or percentage floor carried over from another run.

`GET /api/library/status` exposes `work`, measured `progress`, and `estimate`
per activity. `work.size` is the optional input-volume measure. `estimate`
includes a state, basis, rate/unit and lower/upper seconds when available.
`forecast` is the separate whole-operation range, with inventory status,
remaining/estimated step counts and names of steps still lacking evidence.
`estimated_completion_at` uses the whole-operation point forecast for the native
menu bar when available. An incomplete operation forecast does not publish a
whole-operation completion time; activities without an operation plan retain
their existing stage estimate. `estimated_completion_scope` says which it is
(`operation` or `stage`); the menu says "about N min left" for an operation
and "for this stage" otherwise. Timing history is local metadata; estimation does not read
or send conversation content.

Implementation: `operation_forecast.go`, `activity_progress.go`,
ingestion/capture progress hooks, and
`assets/library.js`. Regression coverage includes uneven transcript sizes,
resumption, warm-up, stale observations, workload changes, history isolation,
concurrent jobs, accessible bars, narrow panels, and upgrade handoffs.

## Whole-operation forecasting and evaluation

An operation declares the work it will do before forecasting: discovery, source
indexing, projections, verification, and the selected analysis steps. Capture &
index also includes capture. Full rebuilds, updates and retained repairs have
separate operation histories. Explicit capture indexing now waits for Git,
tools, authorship and findings through the same coordinator as manual indexing;
reporting complete means those required analyses have finished. Scheduled
incremental runs continue to defer them, so their forecast covers only the work
that the scheduled operation actually performs. Later optional upgrades remain
separate activities.

A dependency graph composes remaining durations: sequential steps add, concurrent
paths take the maximum. Current coordinators execute these steps sequentially;
independently running maintenance is not added to that sequence. Source indexing
can use live pace with a conservative allowance for the source's uncounted tail.
If a stage overruns its prediction, remaining time is revised upward rather than
counting through zero. The progress bar always remains the measured stage count.

With sparse data, each step uses historical duration, adjusted for known input
volume with an allowance for fixed overhead. A single completed peer can supply
a broad **provisional** range. Unknown inventory, fewer than five comparable
samples, or substantial workload extrapolation keep that label. Known empty
inventories use empty peers. If any remaining step has no evidence, the UI
shows how many steps can be estimated and names the missing steps; a partial
sum is never presented as a whole-operation countdown.

After twenty comparable samples, a regularized log-duration regression can use
conversation/group/file counts, bytes or messages, time since prior indexing,
and one-minute machine load. Columns need adequate observations and variation;
missing quantities are not treated as observed zero. Correlated features are
regularized, extrapolation uses broad throughput bounds, and a fit that fails
to improve on the duration baseline is discarded. This fit check is not a
claim of production accuracy: the independent prediction/outcome measurements
below are the evidence for that. Reusable versus invalidated tools/authorship
results and full versus watched-findings passes have separate samples. Byte
bases, extractor versions and analysis versions remain distinct. Identical
source/analysis implementations can share step evidence across entry points;
whole-operation error distributions remain specific to the operation.

History retains eighty runs per operation type on this Mac. Worker events save
at most forty-eight snapshots per run, including launch, discovery, stage
boundaries and periodic progress, whether or not anyone opens the panel.
Completed runs attach actual remaining duration to those predictions. Failed
and interrupted runs retain their predictions but do not supply successful
runtime samples or completion-error scores. Storage uses local metadata and
cached reads; progress callbacks never write to the catalog inside a worker's
transaction.

After eight prior predictions with outcomes, empirical errors can widen the
whole-operation interval. A run never trains or calibrates its own predictions.
These are adaptive ranges, with no promised statistical coverage. Settings →
Capture & index → Performance & verification details reports availability,
median absolute/relative error, interval coverage and width at launch, after
discovery, and during work. Run Details shows predicted versus actual remaining
time; capture/rebuild forecasts are also listed in performance details. The
history API includes `forecast_accuracy` and bounded `forecast_runs` evidence.
Each checkpoint contributes at most one observation per run, so long runs with
many progress reports do not dominate the score.

Production runs are still required to validate useful features, fitted weights,
and interval calibration on real machines. The machinery can learn and expose
its accuracy now; it does not claim high accuracy before that evidence exists.
