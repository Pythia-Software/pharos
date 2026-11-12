# Context, tools and Claude usage estimates

Historical exploratory results. See the later
[v2 deployment selection](claude-estimator-deployment.md) for promoted predictors
and final validation; statements about unchanged weights below describe this
study's original scope.

Exploratory feature-ablation study, November 11, 2026. This extends the
[initial estimation study](claude-chat-estimation.md). It tests the proposed
predictors against recorded coding usage; it does not change the app's fitted
weights or published assumptions. No production catalog records were modified.

The clearest finding is that conversation history and accumulated tool-result
size help predict **processed input**. Tool categories help predict **generated
output**, but conversation position is not a reliable multiplier for generated
tokens. Agent features have mixed incremental value. None of the larger models
provides a dependable replacement for missing thinking clocks or native tool
timing.

## Population and safeguards

The extraction examined 604,877 indexed records in 1,703 native Claude
conversations. It expanded the earlier timer and structural samples to every
preserved request in those conversations through the fixed cutoff
`2026-11-11T16:02:32.718Z`. This is a selected library cohort, not a random sample
of all Claude usage.

After removing 327 duplicate native response IDs, the study contains **116,693
requests, 1,097 linked families and 123,156 tool calls**. The 43,015 helper
requests remain separate model requests. Duplicate copies sometimes disagree
about projected counts: the existing timer copy takes precedence, otherwise the
SDK copy precedes the legacy recording. Entire families linked by a duplicate
ID are joined before splitting, so copies cannot cross training/test partitions.
The original 71,417 eligible positive-thinking timer labels are unchanged.

Two splits test each variant:

- Stable family hash: approximately 60% training, 20% calibration, 20% test.
  The full request test contains 27,635 requests in 210 families; the timer
  test contains 17,840 requests in 188 families.
- Forward-time, disjoint-family stress test: training families finish before
  November 7 at `16:20:43.243Z`; calibration families finish before November 10
  at `22:31:05.644Z`; test families extend beyond that second cutoff. Earlier
  rows of calibration/test families are excluded. The full test contains
  4,147 requests in 201 families; the timer test contains 2,689 requests in
  200 families.

Parent/helper families always stay together. Training gives each family equal
total weight. Ridge regressions predict `log1p(target)`; regularization is chosen
on calibration families, and an independent calibration-set aggregate correction
converts log-centered predictions into candidates for expected totals. This
correction is imperfect, so signed aggregate bias is reported alongside error.
The study also replays the existing arithmetic thinking-rate method separately.

**WAPE** below means `sum(abs(predicted - recorded)) / sum(recorded)`. Lower is
better. It is an aggregate absolute-error measure, not the percentage error of
a typical turn. Paired uncertainty resamples whole families 1,500 times. These
are repeated exploratory comparisons, not a preregistered feature-selection
test. Test results must not become fitted coefficients without fresh validation.

## What the models can observe

All request baselines include recorded model, effort and speed. Generated-output
baselines also include visible assistant text and tool-argument size. Timed
baselines include the recorded thinking interval. The input baseline uses
settings alone. Missing-thinking-duration models receive visible text and
argument size, but never the timer being predicted.

| Predictor group | Available scalar evidence |
| --- | --- |
| Position | Request ordinal, user-turn ordinal, request rounds within the turn |
| Compaction | Recorded boundary, boundaries seen, requests since last boundary |
| Visible history | Cumulative and since-compaction prose, context text and tool arguments |
| Tool counts | Incoming/outgoing calls, nonempty results, short/substantive results, parallel width and accumulated calls |
| Result volume | Incoming and accumulated result bytes, including history since compaction |
| Tool kinds | Incoming/outgoing counts and byte volumes by command, read, edit, search, web, plan, user, metadata, MCP and other categories |
| Agents | Helper status, depth, launches, returned agent results and returned bytes |
| Ancestry sensitivity | Partial parent-chain history and a completeness indicator |

Short results have at most 256 bytes; substantive results exceed 4,096 bytes.
Continuous byte counts are also included, so those thresholds do not stand in
for the complete size signal. Result size measures preserved text, not verified
provider-side tokenization or proof that every byte entered context. Images,
attachments and hidden system prompts require additional assumptions.

The study deliberately excludes `result_tokens`, `carried_tokens`, measured
input/cache counts and measured context growth from predictors. In the current
ledger, even a result-token attribution marked `measured` can be allocated from
the next request's prompt growth; using it to predict input would leak the
answer. Reported token counts appear only as targets.

Only 546 extracted requests have a complete indexed parent chain. Primary
history therefore follows recorded source order within each agent stream;
there are no request/source-order inversions in this extract. This is an
observable history proxy, not exact context reconstruction. Partial ancestry is
tested separately. There are only 123 recorded compaction boundaries in 35
families, so explicit reset behavior has much less independent support than
the overall request count suggests.

The subsequent [response-window study](claude-latency-estimation-study.md)
tests timing features that this structural ablation deliberately omitted:
sender-to-response elapsed time, tool-start positions and residual time after
observed or imputed tool spans.

These are retrospective export features. The current response's tool choices
and argument lengths are available after the response; they cannot be used
unchanged to forecast it before generation. Actual chat exports can also omit
request boundaries and compactions. This benchmark retains coding request
boundaries and does not establish accuracy after those boundaries are hidden.

## Processed input: strong predictive value

Input labels include cached and uncached tokens. They measure total context
processed on each request, not newly added tokens or API charges. There are no
zero or missing input labels in this cohort.

| Added to settings baseline | Family-held-out WAPE | Forward-time WAPE |
| --- | ---: | ---: |
| Nothing | 62.2% | 87.8% |
| Position only | 53.6% | 47.7% |
| Compaction/request-age features only | 30.5% | 26.9% |
| Tool counts only | 29.6% | 26.6% |
| Tool-result volume only | 26.8% | 19.6% |
| Position + compaction + visible history | 30.0% | 24.9% |
| Context + counts + result volume | 25.9% | 19.0% |
| All observable groups, including kinds and agents | 25.7% | 17.0% |

For result volume, paired WAPE improvement over baseline is 35.3 percentage
points in the family test, with a 95% family-bootstrap interval of 32.1–41.0
points; the forward improvement is 68.2 points, interval 51.9–95.5. The all-group
model still underestimates aggregate input by 17.1% in the family test; forward
bias is +0.4%. Improved absolute error does not establish unbiased consumption.

More tool results mean more material that can be carried into subsequent
requests. Position helps approximate that growth, but measured retained text
and result volume are closer to the proposed mechanism. Counts alone can work
as a workload proxy; there is no evidence for a fixed token charge per call.
Tool categories add relatively little after context and volume are present.

Hiding compaction/reset features changes the result substantially. Position,
visible history and result/count features without resets give 53.2% WAPE on
families and 44.4% forward, compared with 25.9% and 19.0% with reset features.
History still helps when boundaries are missing, but the best coding results
must not be quoted as expected accuracy for exports without those boundaries.
Using only immediate result counts/bytes gives 61.0% and 78.7%; accumulated
result-byte features give 50.1% and 56.5%. The evidence favors accumulated
history over looking only at the immediately preceding tool output.

The context/count/volume improvement holds within both roots and helpers:
family WAPE changes from 58.0% to 23.2% for roots and 67.5% to 29.4% for helpers.
Within the largest fixed model/effort/speed group, Opus 5.5/high/standard,
family error changes from 58.6% to 23.5%, and forward error from 96.2% to 13.9%.
It is therefore not explained solely by mixing different model settings or
different agent roles. These subgroup metrics reuse the full training fit;
they are not separately tuned subgroup models.

## Generated output: tool kinds help; position is inconsistent

| Output model | Family-held-out WAPE | Forward-time WAPE |
| --- | ---: | ---: |
| No timer: settings + visible text + argument size | 77.1% | 81.9% |
| Same baseline + tool kinds | 65.0% | 76.8% |
| Same baseline + position only | 77.3% | 82.1% |
| Same baseline + all observable groups | 172.7% | 75.5% |
| With timer: settings + visible text + argument size + timer | 40.6% | 32.3% |
| Timed baseline + tool kinds | 37.8% | 25.7% |
| Timed baseline + all observable groups | 33.5% | 35.5% |

Without timers, tool kinds reduce family-test WAPE by 12.1 points (bootstrap
interval 7.7–15.0) and forward WAPE by 5.2 points (4.2–6.4). Timed output improves
by 2.8 points on families (interval includes no improvement) and 6.6 points
forward (5.3–8.0). These effects are incremental to visible output and argument
size, but may still reflect task mix or tokenization differences rather than
an intrinsic per-tool reasoning cost.

The large model's 172.7% error shows why adding every feature is unsafe: its
log-centered prediction can look better while arithmetic expected totals become
unstable. The current retransformation correction does not solve that problem.
The opposite directions across splits also prevent claiming a general output
benefit from accumulated context.

Observed self-turn sums offer a further warning. Without timers, tool kinds
improve WAPE from 29.2% to 23.8% across 591 observed ending turns in the family
test, but worsen it from 55.3% to 57.2% across 226 forward turns. These sums
include observed requests within a conversation and an ending response without
outgoing calls; they are not guaranteed complete parent-plus-helper task totals.
Request-level gains are therefore not yet validated chat-turn gains.

## Thinking and tool latency

When a native thinking timer exists, extra structural features do not reliably
beat the existing arithmetic rate method. That method has 32.2% WAPE in the
revised family split and 23.9% forward. Result volume reaches 31.6% on the family
test but 92.9% forward; the larger structural models also deteriorate forward.
Almost half the forward timer requests have model/effort/speed combinations
absent from training. Known-setting sensitivity is necessary before attributing
that failure only to context. Even among known forward settings, the large
model worsens WAPE from 36.0% to 63.3%, while a small cumulative-result-byte
variant improves it to 26.7%. Some narrower predictors deserve further tests;
the existing fallback policy remains essential.

Predicting missing thinking duration is much weaker: baseline WAPE is 83.4%
on families and 92.7% forward. All observable groups yield 160.9% and 89.5%.
These models condition on positive recorded thinking and cannot establish
whether an unobserved trace represents zero thinking. They do not justify
replacing explicit missing-clock scenarios with a precise inferred timer.

The tool experiment uses 122,256 recorded call-to-result spans, excluding agent
and user categories and delegation records. It starts with exact tool name and
command category, then adds position, argument/result volume, status and agent
depth. WAPE changes from 159.3% to 154.8% on families and 114.2% to 109.0%
forward. These are modest improvements against very large remaining error.

The duration distribution is highly skewed: median 0.108 seconds, p90 10.796
seconds, p99 364.166 seconds, mean 12.222 seconds and maximum 19,897.930 seconds.
Most calls are shell commands. The targets include result-observation and
harness delay, rather than pure model latency; background acknowledgments
measure acknowledgment spans, not the eventual process lifetime. Tool result
size is retrospective evidence. These coding results do not replace native
chat-tool priors or validate coding/chat implementation interchangeability.

## Recommended parameterized design

Use separate models and explicit evidence requirements for each metric:

1. **Processed input:** estimate context retained in each agent stream, using
   visible text, arguments, tool-result volume and request age. Reset at a
   recorded compaction. When boundaries are missing, publish retention/reset
   scenarios rather than assuming a recorded reset. Sum estimated context over
   model requests to obtain processed input; the last context size alone is not
   a turn's input usage. Estimate hidden request-count scenarios separately.
2. **Generated output:** retain the visible-output plus thinking model. Test
   small tool-category adjustments separately, especially where clocks are
   missing. A conversation-position multiplier is not supported. Require
   improved arithmetic totals and forward validation before promoting weights.
3. **Sub-agents:** maintain independent contexts and their own model/effort
   profiles. Parent input includes returned text, not the helper's entire
   transcript. Count helper usage once in an explicitly scoped family total;
   a launch acknowledgment does not reveal completed helper usage. Agent
   topology is necessary accounting structure even when a universal statistical
   multiplier adds little value.
4. **Latency:** preserve native spans and runtime-specific tool priors. Keep
   missing thinking intervals as broad scenarios. Use dependency order and
   parallel overlap for turn elapsed time; summing every tool's duration would
   overcount concurrent work.

Future published context/structural profiles should live in the global manifest
with feature-schema and fit versions, explicit model/effort/speed fallback,
source support, missing-feature scenarios and validation summaries. A library
with no matching coding history must still work. Local calibration may refine
supported groups; unsupported or drifting fits fall back to published profiles.
Do not use a fitted zero for an unavailable feature or silently assign an
unknown setting to a known model group.

Promotion needs an export-shaped benchmark that masks request boundaries and
compactions according to actual chat coverage, plus fresh family and time
holdouts. The present result supports adding a separate context estimator and
testing small output adjustments; it does not validate deployment of the full
regression as a chat estimator.

## Reproduction

Private scalar extracts and scripts remain under `.context/`:
`claude-context-extract.py`, `claude-context-features.json`,
`claude-context-duplicates.json`, `claude-context-ablation.py` and
`claude-context-sensitivity.py`, with their result JSON files. Extraction uses
read-only, per-conversation SQLite transactions. No transcript bodies, commands
or tool-result text are included in the feature files. The extraction is
scalar-only but still contains private entity identities; do not publish it.

The [aggregate results](../estimation/studies/claude-context-2026-11-11.json)
contain no transcript or entity identities. They are study evidence and are
not loaded by the app or used as manifest coefficients.
