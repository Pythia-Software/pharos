# Response windows, tool positions and thinking estimates

Historical exploratory results. The later
[v2 deployment selection](claude-estimator-deployment.md) records which timing
models were promoted, their source guards and final independent evaluation.
Statements about unchanged runtime weights below describe this study's scope.

Exploratory study, November 12, 2026. This extends the
[context/tool study](claude-context-estimation-study.md), using the same frozen
coding cohort and family-disjoint evaluation. The new question is whether the
elapsed sender-to-response window, and time occupied by tools within it, improve
estimates of thinking tokens. No production data or runtime weights changed.

**Yes: elapsed time adds predictive value, and observed tool spans make the
remaining time substantially more informative. Replacing tool spans with typical
durations gives smaller, less consistent gains.** The remaining time is a
predictor of thinking, not a direct measurement of it.

## Two clocks for two counting units

A turn's elapsed time runs from its sender input to its last observed assistant
response event. A turn can contain many model requests. Its thinking-token
target is the sum of its own requests, without automatically including helpers.

For an individual request, restart the window after the previous retained
response event or the new sender input. A recorded incoming tool result can
then identify the later point at which the next model cycle could consume it.
The original user-to-response gap is cumulative and becomes a poor predictor
of thinking tokens in each successive request.

This distinction is visible in the positive-thinking, timer-labeled coding
test. Spearman correlations with thinking tokens are:

| Clock feature | Family holdout | Forward-time holdout |
| --- | ---: | ---: |
| Cumulative sender-to-response time | 0.072 | 0.211 |
| Local response window | 0.637 | 0.745 |
| Time after latest recorded tool result | 0.853 | 0.927 |

For whole turns, elapsed time correlates at 0.722/0.830 and the residual after
recorded tool spans at 0.847/0.927. The final post-tool gap alone correlates at
only 0.489/0.175, because it omits earlier model work within the turn. A useful
request feature is therefore not automatically a whole-turn estimator.

## Population, labels and evaluation

The earlier 116,693 deduplicated requests remain the frozen target population.
Read-only queries add sender and response-event clocks and tool start/result
clocks. All 1,684 conversations examined for these additions have one recorded
request stream; none required a multi-stream exclusion. No frozen usage label
or setting changed in this readback. There are usable sender/response clocks
for 116,292 requests in 1,090 linked families; 401 lack a required anchor.

The closed-turn cohort contains **2,390 turns in 963 families**. Requirements:

- A recorded sender message and an ending assistant response with visible text
  and no outstanding outgoing call in that response.
- Every native request in that sender interval appears in the frozen sample,
  every thinking-token label is known, and the turn uses one model/effort/speed
  combination. Explicit zero labels remain eligible: 37 turns have zero.
- A nonnegative observed window from 100 ms through 30 minutes, valid call and
  result clocks, and no recorded user-interaction tool in the turn.

There are 2,339 turns with tools and 345 helper turns. The median turn has 15
requests and a 282.6-second observed window. Exclusions include 347 windows
outside the duration range, 215 turns without an observed ending response,
94 with unknown thinking labels, four with user-interaction tools, and one
with an invalid/missing tool clock. These eligibility rules favor well-recorded,
completed work; they are not coverage claims about every exported conversation.

The local-request comparison contains **104,006 requests in 1,073 families**,
including explicit zero-thinking requests. It requires a 100-ms to 600-second
local window and valid incoming tool clocks, so the native-clock and masked-end
variants can use exactly the same cases. A positive-thinking, native-timer
control contains 64,873 requests. The total extraction is scalar-only and
contains no transcript bodies or commands.

Splits follow the earlier study: stable family hash, approximately 60/20/20,
and the fixed disjoint-family forward-time split at November 7
`16:20:43.243Z` and November 10 `22:31:05.644Z`. The turn tests contain 484 turns
in the family split and 222 forward. Parent/helper families and duplicated
recordings stay together. Whole-family bootstrap comparisons use 1,500 draws.

**WAPE** is total absolute error divided by total recorded target tokens.
It describes aggregate error, not the typical percentage error of one turn.
All comparisons are exploratory. Calibration, missingness and temporal drift
remain separate from the existence of a useful clock signal.

## What “unexplained time” means

For a window from `a` to `b`, each tool contributes an interval from its recorded
start to either its observed result or an imputed result time. Clip intervals
to the window and take their **union**, so overlapping tools are counted once:

```text
tool-covered time = length(union(tool intervals within [a, b]))
residual time     = max(0, (b - a) - tool-covered time)
thinking tokens   = fitted(model, effort, residual, elapsed, visible output,
                          tool categories, tool positions, evidence coverage)
```

Tool position features include first/last start offsets, normalized positions
within the window, spread, largest gap between starts, time before the first
call and time after the last start. These use starts only.

In the masked-end experiments, every held-out result clock is hidden from
these features. Tool durations come only from training families: exact
tool/command category over operation category over a global fallback, with
20-call/five-family support gates and shrinkage by 20 prior-equivalent families.
Arithmetic expected and median duration priors are tested separately. The
observed-span variant is an upper-information comparison using actual ends.

An interval union built from mean durations is a deterministic feature, not
the expected union of random tool intervals. Clipping and overlap are nonlinear.
A later probabilistic implementation should sample duration scenarios and
propagate their overlap rather than subtract a sum of means and label the
remainder as exact inference time.

The residual also includes text/argument generation, prompt processing,
queueing, harness/network delay and unrecorded pauses. Model work can overlap
tool execution. Conversely, an overestimated tool prior can consume the whole
window even when thinking occurred. A zero residual must not establish zero
thinking. Recorded background acknowledgments describe acknowledgment time;
they do not reveal a background process's complete lifetime.

## Turn-level parameterized regressions

The structural baseline includes model/effort/speed, visible assistant text,
argument size, tool counts/categories and helper status. It deliberately omits
the native model-request count, which chat exports do not expose. The fitting
procedure is the earlier family-balanced ridge regression on `log1p(target)`,
with regularization and aggregate retransformation correction selected from
calibration families only.

| Thinking tokens per observed turn | Family WAPE | Forward WAPE |
| --- | ---: | ---: |
| Structural baseline | 75.8% | 87.8% |
| Add sender-to-response elapsed time | 66.6% | 83.6% |
| Add elapsed time and tool positions | 63.7% | 83.3% |
| Add elapsed time and expected-tool residual | 63.3% | 84.7% |
| Add elapsed time, residual and positions | 58.5% | 84.4% |
| Add elapsed time and observed-tool residual | 49.5% | 72.3% |

Elapsed time improves family error by 9.2 percentage points, with a bootstrap
interval of 3.0–20.4 points. Adding residual and positions improves a further
8.1 points over elapsed alone, interval 2.3–16.7. That extra benefit does not
transfer forward: expected-tool residual and positions worsen error by 0.8
points relative to elapsed alone, interval 0.4–1.2.

Generated output also benefits in this cohort: family WAPE changes from 35.5%
to 32.0% with elapsed, 28.1% with positions, and 25.8% with residual plus
positions. Forward errors are 67.7%, 62.3%, 57.1% and 58.9%, respectively.
Thus positions add information beyond elapsed for output in both splits;
subtracting estimated waits is not consistently better than positions alone.

These regressions have substantial forward underprediction even among known
settings. For thinking, the 109 forward turns with settings seen in training
have 84.4% baseline error, 79.8% with elapsed and 68.0% with observed residual.
The full forward turn test also contains 113 unseen-setting turns. These fits
must not silently replace the published fallback profiles.

## Check against a simpler arithmetic model

Log-regression expected totals were unstable when requests with zero and
positive thinking were mixed. For example, adding a local window worsened
family request WAPE from 230.8% to 312.1%. A separate sensitivity uses bounded
conditional arithmetic means to check the timing signal without exponentiated
tails. It does not contain visible-output or tool-category regression terms.

The sensitivity averages per-family mean token counts within duration bands,
then shrinks exact setting/band estimates toward effort/band and global/band
estimates. It uses 20-row/five-family gates, 20 prior-equivalent families, and
a calibration-only aggregate correction. Duration edges are 0.1, 1, 3, 10, 30,
100, 300, 600 and 1,800 seconds. It does not treat an absent trace as zero.

| Thinking tokens per request, including reported zeros | Family WAPE | Forward WAPE |
| --- | ---: | ---: |
| Settings-only arithmetic mean | 121.0% | 110.9% |
| Local elapsed window | 94.7% | 98.5% |
| Residual using training-only expected tool durations | 90.7% | 88.9% |
| Residual using observed tool spans | 56.3% | 40.3% |

In this sensitivity, start-only residuals improve over elapsed alone by 4.0
points on families (interval 1.3–8.4) and 9.6 forward (7.9–11.4). With actual
ends, the gains are 38.4 points (33.5–46.1) and 58.2 points (56.1–60.6).
Observed-span aggregate bias remains -4.8%/-15.3%; the start-only variant has
-7.7%/-50.0% bias. Missing tool ends therefore leave substantial uncertainty.

Restricting to positive-thinking requests with native timer labels gives
76.2%/80.8% WAPE for elapsed, 71.6%/79.0% for expected-tool residual and
44.5%/32.3% for observed-span residual. The direction survives removal of
zero-thinking cases. At turn grain, the arithmetic sensitivity improves from
85.4%/67.0% with settings to 64.5%/53.2% with elapsed and 53.5%/45.9% with
observed residual. Expected-tool residual gives 68.6%/51.6%, again mixed
relative to elapsed alone. Results from the two model classes are separate
experiments, not interchangeable baselines or a selected production fit.

## Actual export clocks need their own calibration

The original export has 449 assistant messages. Parent links identify the
sender, but message-envelope clocks and block clocks have different meanings.
In 110 clocked responses, a block precedes the parent sender's `created_at`
by 1.6–152 ms (median 6.5 ms), consistent with small clock-domain differences.
Assistant `created_at` is also not a guaranteed generation-completion clock:
relative to the final native stop, its range in 277 parent-linked clocked
responses is -708.6 to +122.5 seconds, despite a median difference of +0.096 s.

The export check predicts **observed thinking-interval duration**, since true
chat thinking-token labels are absent. The baseline includes summary count,
summary length, thinking-block count and visible text/arguments/tool categories.
The broad clock-jitter sensitivity allows at most 250 ms of sender/block skew
and widens the start to the earliest native block; it never clips thinking away
to fit the envelope. This produces 251 windows in 135 conversations, with 52
held-out windows in 25 conversations. Adding the raw sender-to-assistant
message-envelope gap changes WAPE from 60.0% to 70.4%. Adding a window ending
at a native block stop gives 48.1%. Neither bootstrap comparison establishes
a dependable improvement in this small sample.

Requiring every tool start leaves 186 windows in 109 conversations, including
105 windows with tools. With ends masked, 28 held-out windows have 60.9%
baseline WAPE, 57.8% with native elapsed and 40.3% with expected-tool residual.
All improvement intervals include no improvement. Only 153 windows have all
tool completions observed, including 72 with tools; their paired test has just
17 windows. A strict sender-clock run retains only 83 windows and yields
different error directions. These small, coverage-sensitive checks are
descriptive evidence, not validated chat-token accuracy.

The useful distinction is an explicitly sourced **native event window** versus
a **message-envelope gap proxy**. The latter can remain an optional weak feature
when no block clocks exist, but cannot supply a hard elapsed-time budget or be
silently interpreted as request latency. `updated_at` remains unsuitable as a
completion clock.

## Pipeline consequence

Add a versioned response-window feature family with its clock provenance,
start/end coverage, tool start offsets, observed and prior-based interval
coverage, and residual sensitivity scenarios. Keep the metrics distinct:

1. Prefer existing native thinking intervals where present. Use the residual
   to refine missing portions or a whole-turn missing-trace scenario. Do not
   add a whole-turn residual prediction to already counted thinking tokens.
2. Fit expected thinking tokens conditional on model/effort, elapsed, residual,
   visible output and tool positions. Compare a bounded arithmetic estimator
   with any regression before promoting its coefficients. Preserve zero labels
   and unknown labels separately.
3. Use runtime-specific tool duration priors and interval overlap. Async launches,
   waits, user interactions and helpers require their own semantics. Keep a
   helper's usage separate from the parent's elapsed window and returned text.
4. For exports with hidden request boundaries, estimate a complete observed
   turn once. Never apply the cumulative user-to-response window repeatedly to
   its inferred requests or thinking blocks.
5. Publish supported response-window assumptions in the global manifest, with
   model/effort fallback, evidence regime, fit version and sensitivity ranges.
   Local coding records can refine supported groups; libraries without coding
   records retain published assumptions. Unavailable or incompatible clocks
   use existing summary/block/missing-trace fallbacks.

This supports retaining elapsed and residual time as model inputs, with stronger
weight for native result clocks and broader scenarios for inferred tool ends.
The remaining work before a runtime change is an export-shaped benchmark for
the selected fallback model and clock regime, rather than proof that chat and
coding deployments are identical.

## Reproduction and artifacts

The [aggregate results](../estimation/studies/claude-latency-2026-11-12.json)
contain no transcript bodies or entity identities and are not loaded by the
app. Private scalar extracts, scripts and detailed results remain in `.context/`:
`claude-latency-extract.py`, `claude-latency-ablation.py`,
`claude-latency-mean-check.py` and `claude-latency-chat-check.py`, with matching
JSON/log files. The strict chat-clock sensitivity is retained separately as
`claude-latency-chat-strict-check.json`. Source reads use SQLite read-only mode,
`query_only`, bounded statements and per-conversation snapshots.
