# Progress and time estimates

The drive panel shows each active job and its current stage, source, elapsed
time, processed workload, and known follow-up work. A bar measures the named
stage. It does not promise that the entire library update is that far through.
Concurrent indexing and maintenance retain separate bars and estimates.

Capture measures bytes copied for the current source's file-copy stage.
Database snapshots and saving the manifest are separate stages. Indexing a
capture counts the changed conversation groups before parsing them (one group
can include a parent session and its subagents). That same discovery pass also
collects transcript sizes. Where possible, the bar and ETA use bytes for files
or message counts for database sessions, so a few large conversations carry
more weight than many small ones. Mixed or unavailable size signals fall back
to group counts. The panel shows both group counts and input volume when known.
Counts advance only after a record is successfully handled.

Incremental indexing discovers and processes changes in one pass. It reports
input groups checked, groups processed, and messages written without adding a
second filesystem scan just to calculate a percentage. Verification, backups,
Git lookups, findings, and other stages with no complete denominator explain
what they are doing and leave the remaining time unknown. Upgrade steps and
tool-ledger/search backfills with known counts can estimate their own pace.

Estimates are ranges for the current stage, with the basis visible below the
bar. They are rough bounds, not calibrated statistical confidence intervals:

- Live estimates need at least eight seconds and three sampled observations
  with advancing counts. They use about a minute of recent work, including time spent on the
  current item. Variable speeds widen the range.
- Before there is enough live data, measured capture-copy and capture-index
  stages can use the median throughput of at least three comparable successful
  stages on this Mac. History retains the last twenty samples and separates
  job type, provider, indexer version, rebuild versus update, stage, and
  measurement unit. Samples
  outside one quarter to four times the current workload are excluded.
- Once live speed is measurable it gets at least 75% of the weight, with the
  historical rate bounded to half to twice that speed. History loses its
  influence over the first thirty seconds of observed progress.
  Whole-run durations do not supply ETAs, and are no longer recorded,
  because they did not record how much work was performed.
- A new job, source, stage, measurement unit, changed total, or reset counter
  restarts live measurement. Work completed before observation or resumption
  is not credited to the new sample window.
- After at least 45 seconds with no completed work (longer for slow counted
  units), the countdown is withdrawn and the panel explains that the current
  item may be large or waiting. Fresh progress allows estimation again.
- A stop request replaces the estimate with a safe-stopping status. A failed
  status request hides estimates until the service can be reached again.

Completing a measured stage does not imply the job is done: identity linking,
retained snapshots, flushing, and maintenance can still follow. The UI has no
fixed capture/index/upgrade percentages, elapsed-time animation toward 100%,
or percentage floor carried over from another run.

`GET /api/library/status` exposes `work`, measured `progress`, and `estimate`
per activity. `work.size` is the optional input-volume measure. `estimate`
includes a state, basis, rate/unit and lower/upper seconds when available.
`estimated_completion_at` remains for the native menu bar and represents the
current stage only. Timing history is local metadata; estimation does not read
or send conversation content.

Implementation: `activity_progress.go`, ingestion/capture progress hooks, and
`assets/library.js`. Regression coverage includes uneven transcript sizes,
resumption, warm-up, stale observations, workload changes, history isolation,
concurrent jobs, accessible bars, narrow panels, and upgrade handoffs.
