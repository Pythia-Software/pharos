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
  A new indexer version starts with no samples of its own, so until it has
  three it reads the previous versions' history for the same provider, mode
  and host, kept in a second row beside the exact one.
- Once live speed is measurable it gets at least 75% of the weight, with the
  historical rate bounded to half to twice that speed. History loses its
  influence over the first thirty seconds of observed progress.
  Legacy whole-run durations do not supply these stage throughput estimates,
  because they did not record how much work was performed.
- A new job, source, stage, measurement unit, or reset counter restarts live
  measurement. A total that grows while its counter goes on does not: a queue
  that grows as it drains, or an inventory that is counted again, keeps the
  measured pace and only the remaining time follows the new total. Work
  completed before observation or resumption
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
Settings → Background Processing → run Details exposes these measurements.
Interrupted stages remain in run history without becoming completed ETA
samples. Workload features also feed the operation model described below.

Completing a measured stage does not imply the job is done: identity linking,
retained snapshots, flushing, and maintenance can still follow. The UI has no
fixed capture/index/upgrade percentages, elapsed-time animation toward 100%,
or percentage floor carried over from another run.

The unified Background Processing panel shows every running operation and its
active stage. Maintenance batches expose their inventory and committed counts
in the same operation tree as indexing and analysis work. Queue inventories
can grow when new work arrives; their fractions measure processed work units,
not elapsed time. A named phase with an unknown total still shows completed
counts and an accessible indeterminate indicator. Query pauses and catalog
writer waits retain the last committed counts and say why work is waiting.
Analysis progress reaches its stage operation whether or not a run-history
meter is attached. Unknown phases never borrow a previous stage's percentage.

Local backups report catalog snapshot bytes while SQLite copies page batches,
using the page size and total of the pinned read transaction. The panel receives
an initial inventory, the first copied batch, periodic updates and the final
count. A retried snapshot resets its counters; the completed catalog is counted
once before capture and other file sizes are added. A completed snapshot still
needs to be saved durably, and the remaining library files may follow.

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
If a stage overruns its prediction, it is expected to take half as long again
as it has taken so far, rather than counting through zero. A step that failed
is not retried in the run, so it stops counting as remaining work. The progress
bar always remains the measured stage count.

Each step is forecast from the forty most recent completed steps with its ID,
whatever became of the run they were in: the steps an interrupted or failed run
did finish are measured durations. The step a run was stopped in is not one;
its counted and total units are stored with it.

- A step without a known quantity uses the median of those durations, with
  their 10th and 90th percentiles as its range.
- A step with a quantity is a fixed cost plus a cost per unit, fitted robustly
  (the median pairwise slope, then the median intercept). Its range is the
  10th to 90th percentile of its peers' ratios to that fit. An incremental
  source is measured in conversation groups, which predict its duration
  better than bytes do: an update re-derives the whole stored conversation,
  and transcript bytes are whole-file lengths. A full index of captured inputs
  keeps bytes or messages, and never mixes file bytes with decoded database
  text.
- A known empty step reads only empty peers, and a real one only real peers.
  The verification step declares whether it has an eligible input when it
  starts, for the same reason.
- Fewer than three peers give a broad **provisional** range of a third to
  three times the median. Unknown inventory, fewer than five peers, a quantity
  beyond four times what was measured, and an overrunning step keep that
  label.
- Extractor and analysis versions are part of a step's key, and a new version
  can cost very differently. Samples of the current version are used alone as
  soon as one exists. Until then the newest samples of earlier versions stand
  in, marked provisional, so a version change does not withdraw the countdown.
- Reusable versus invalidated tools/authorship results and full versus
  watched-findings passes have separate samples. Identical source/analysis
  implementations share step evidence across entry points; whole-operation
  error distributions remain specific to the operation.

If any remaining step has no evidence at all, the UI shows how many steps can
be estimated and names the missing steps; a partial sum is never presented as a
whole-operation countdown. Machine load and time since the previous index are
recorded with each step but are not model inputs: on the measured library they
did not improve the forecast, and load is mostly Pharos's own work.

History retains eighty runs per operation type on this Mac. Worker events save
at most forty-eight snapshots per run, including launch, discovery, stage
boundaries and periodic progress, whether or not anyone opens the panel.
Completed runs attach actual remaining duration to those predictions. Failed
and interrupted runs retain their predictions and train the step models with
the steps they completed, but do not supply completion-error scores. Storage
uses local metadata and cached reads; progress callbacks never write to the
catalog inside a worker's transaction.

The whole-operation range does not add up the steps' ranges: one step that is
either nothing or real work would then set the width of the whole operation.
It is the point forecast scaled by how previous runs of the same operation
ended relative to their own forecast at the same checkpoint (launch, after
discovery, or at each quarter of the run), aiming at about 80% coverage:

- with twenty or more previous outcomes, their 10th and 90th percentiles;
- with eight to nineteen, the 5th and 95th;
- with fewer, half to twice the point, extended by the summed step ranges.

The range therefore narrows as well as widens. A run never trains or
calibrates its own predictions, and only the forty most recent outcomes count.
Each stored snapshot names the estimator that made it (`estimator`), and
calibration and the accuracy report read only the current one's, so an older
model's intervals never calibrate this one. The stored rows keep their key and
shape: an older Pharos sharing the library reads them, ignores the newer fields
and drops them from the rows it rewrites, which costs calibration samples only.

Before discovery an automatic run is either nothing to do (a few seconds) or
real work (a minute or more), and no range describes both. While the inventory
is unknown and the range's high is more than ten times its low, the Background
Processing panel, the drive panel and the menu bar show the step being checked
and no time. Stored snapshots keep the numbers, to score that checkpoint.

Replayed over three weeks of one library's history, each run forecast only
from the runs before it (`TestOperationForecastReplayKeepsCalibratedNarrowRanges`
runs the same replay on a reduced record, and
`operation_forecast_probe_test.go` on a copy of a library), the range after
discovery covered the actual remaining time in 83% of 70 automatic runs at a
median width of 1.2 times that time, where the previous model's covered 100%
at 281 times. Manual Index changes covered 50–76% on 17 runs that grew
several-fold over the period: with so few outcomes the range is too narrow
there, and the "early estimate" label is the honest reading. These are adaptive
ranges, with no promised statistical coverage. Settings →
Capture & index → Performance & verification details reports availability,
median absolute/relative error, interval coverage and width at launch, after
discovery, and during work. Run Details shows predicted versus actual remaining
time; capture/rebuild forecasts are also listed in performance details. The
history API includes `forecast_accuracy` and bounded `forecast_runs` evidence.
Each checkpoint contributes at most one observation per run, so long runs with
many progress reports do not dominate the score.

The point forecast is still off by about a fifth to a third at the median:
findings and authorship vary without a quantity that explains them. Other
libraries and machines need their own outcomes before these figures say
anything about them; the machinery exposes its accuracy and does not claim
more than that evidence shows.
