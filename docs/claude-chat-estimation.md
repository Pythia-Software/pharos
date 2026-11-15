# Claude chat token and latency estimation

Historical survey and v1 implementation notes. The later
[v2 deployment selection](claude-estimator-deployment.md) supersedes the initial
runtime behavior described here and records the final independent evaluation.

Research and initial implementation, 2026-11-11. This survey uses the original
collected Claude export and read-only production catalog queries. No synthetic
conversation supplied the empirical results. Private source identifiers and
numeric extracts remain in `.context/`; no production data was reindexed or
changed during the study. The initial in-app pipeline now implements the
published-prior and local thinking-rate paths described below; its implementation
contract is in [the estimation manifest guide](../estimation/README.md).

The follow-up [context and tool feature study](claude-context-estimation-study.md)
tests conversation position, compaction, accumulated result volume, tool kinds
and helper activity on 116,693 deduplicated coding requests. It finds a strong
input-context signal and narrower output-category gains, with important
forward-time and missing-boundary limits. Those exploratory fits were not
deployed directly; the final selection tests and promotes a bounded subset.

The [response-window study](claude-latency-estimation-study.md) then tests
sender-to-response elapsed time, tool positions and residual time after observed
or estimated tool spans. Elapsed time adds useful signal; actual tool-result
clocks are much stronger than inferred completions. Message-envelope gaps are
tested separately from native event windows. The later deployment selection
records which follow-up fits became published weights.

## Initial pipeline

The reader now exposes separate generated-output and per-tool estimates for
Claude.ai exports. A versioned global manifest ships with the app, with explicit
model/effort/speed rules, fallback profiles, sensitivity ranges, provenance and
runtime-specific tool assumptions. It works in a library without local coding
history. Compatible local coding evidence can refine supported thinking-rate
groups through bounded Tools maintenance; it never rewrites the published
manifest. Read-time inference uses current preserved source blocks before
reader clipping. Reported token usage, API costs and measured tool durations
remain separate.

This version uses arithmetic, family-balanced rates within duration bands,
shrunk toward published priors. An exploratory replay fitted both priors and
refinements on training families only, avoiding leakage from the shipped study
priors. Across the same 17,959 held-out requests from 188 families, it has
1.371× median factor error, 79.3% within 2×, 32.1% weighted absolute percentage
error and **15.9% aggregate underprediction**. The arithmetic approach reduces
the earlier log-model's aggregate bias but does not eliminate it. This replay
does not validate chat token accuracy or sensitivity-bound coverage.

The first version does not reconstruct hidden model-request counts, estimate
processed input/API costs, infer historical execution from later selected-model
observations, or claim complete turn latency. Missing-trace and missing-clock
cases use explicit published assumptions. Native result clocks establish
observed tool spans; completed unclocked calls use published runtime priors;
async acknowledgments and missing results remain unavailable. The later stages
below remain the roadmap rather than claims about implemented behavior.

A read-only implementation probe against the existing indexed library produced
estimates for 444 selected exported responses across 168 conversations, including
396 backed by older retained blocks without a message envelope. One assistant
record lacked supported content. The probe distinguished 218 native observed
tool spans, 679 prior-based durations and 20 unavailable completions. The feature
and inference pass took about 112 ms on this Mac. These are dated library
coverage/timing observations, not a new token-accuracy benchmark or a universal
performance guarantee. No production catalog writes or reindexing were performed.

## Recommendation

Build a versioned estimation layer over preserved evidence. Its strongest first
target is **generated output tokens per turn**, using observed thinking duration
and visible response/tool-input size, conditioned on recorded model and effort.
Estimate tool execution separately by runtime and operation, then reconstruct
the turn's elapsed time from dependencies and recorded clocks.

Use matching coding models as the working prior, as requested. The engineering
question is how well the available signals predict usage under that assumption;
establishing identical chat and coding deployments is outside this work. Keep
coding validation error and uncertainty about applying it to chat separately
visible. Neither should prevent producing a useful best guess.

Produce separate metrics for generated output, processed input, tool execution,
model waiting and turn elapsed time. An exported assistant message can contain
many model requests. A tool call, thinking summary and model request are three
different counting units.

## Study population and method

The source export contains 168 conversations, 912 messages and 3,796 content
blocks, spanning message creation dates from April 23 through November 10, 2026.
All conversation blocks were inspected directly in the preserved ZIP. This
extends the [export coverage audit](claude-export-audit.md) with timing coverage.

The structural coding sample contains 288 native Claude Code conversations:
up to 12 conversations chosen by stable ID hash from each of the 24 largest
conversation model/effort groups. It includes 147 root conversations and 141
helpers, 9,633 model requests, 11,055 tool calls and 57,540 indexed records.
Native recordings were preferred over Conductor mirrors. All sampled requests
were retained; the extraction has a first/last-600 cap for sessions exceeding
1,200 requests. These stratified summaries are descriptive, not estimates of
the whole library's workload mixture.

The sample's request models include Opus 4.6, 4.7, 4.8, 5 and 5.5; Sonnet 4.6,
5 and 5.5; Haiku 4.5; and Fable 5.1 and 5.2. Names here are recorded model
identities. Request-level identity is used because conversations can switch
model or effort. Placeholder models were excluded when selecting the native
sample. Another 24 Antigravity conversations and one Cursor conversation have
Claude model labels; they were identified but were not calibration donors.

A second extraction expands the timer study to every native Claude Code
request with currently projected `thinkingDurationMs` evidence: 71,648 requests
in 1,481 conversations. Of these, 71,541 have positive reported reasoning
tokens. Requiring a timer from 100 ms through 600 seconds excludes 124
sub-100-ms records, leaving **71,417 requests, 1,479 conversations and 996
linked conversation families**. There are no repeated API response IDs in this
extraction. Its observed dates are October 2 through November 11, 2026.

The 100-ms boundary is a diagnostic eligibility rule, not a correction of
the original timer. Sub-100-ms records remain evidence and require a separate
small-duration model. Some carry implausibly high apparent token rates.

Queries used SQLite `mode=ro` and `query_only`, with bounded statements.
The catalog was live, rather than a frozen database snapshot; these are dated
observations, not a transactionally frozen whole-library census.

## What each format supplies

| Pathway | Useful native evidence | Estimation consequence |
| --- | --- | --- |
| Claude.ai conversation export | Parent-linked message tree; ordered content; tool IDs and inputs/results; thinking text/summaries; some block start/stop clocks | Reconstruct a turn and its partially observed timeline. No native request boundaries or usage in this export. |
| Native Claude Code JSONL | Event UUID, parent UUID, API message ID, execution model, usage snapshots, tool IDs/results, event timestamps; newer effort, reasoning-token details and thinking timers | Primary donor for request usage and timer calibration. Reconcile streamed snapshots before measuring. |
| Conductor Claude recording | SDK event envelopes and usage; session effort settings; delegated streams | Useful donor or supplementary metadata after explicit mirror deduplication. Session settings have weaker time scope than per-request settings. |
| Claude Code cloud supplement in this export | 31 fragments from one session, fragment creation time, roles, continuation `part`/`parts`; 17 tool-use and seven tool-result blocks | Useful structural evidence, not a measured-usage donor here. No model, effort or usage in these fragment envelopes. |
| Claude chat settings observations | Selected model, effort/thinking settings, conversation identity and observation provenance | Future conditioning input. A later observed selection does not establish the model used for an earlier turn. |

Native coding content and chat-export content do not have identical visibility.
Anthropic documents that returned thinking can be summarized or omitted, and
usage counts the full thinking rather than the displayed summary. Some recent
models also expose progress updates as thinking blocks. Consequently even
coding `reasoning_format=text` does not establish full internal thinking.
[Thinking documentation](https://platform.claude.com/docs/en/build-with-claude/thinking).

### Chat clocks are partial but substantial

| Content | Blocks | Native start | Native stop |
| --- | ---: | ---: | ---: |
| Thinking | 567 | 567 | 567 |
| Tool use | 913 | 461 | 273 |
| Tool result | 912 | 223 | 223 |
| Text | 1,089 | 552 | 552 |

All 567 thinking blocks have both clocks. Their recorded interval has a median
of **5.33 seconds**, p90 **34.37 seconds**, and p95 **51.89 seconds**. Treat this
as a recorded thinking-block interval, which can include display/transport
effects, rather than pure inference time.

There are 1,094 summary entries in 510 thinking blocks. Each summary entry has
only `summary` text: **no separate timestamp**. The timestamp belongs to its
enclosing thinking block. Nonempty `thinking` text exists in 159 blocks and can
coexist with summaries; these counts are not disjoint.

Summary count correlates with enclosing duration (Spearman 0.712); summary
character count correlates at 0.690. Median block durations by summary count:

| Summaries in block | Blocks | Median recorded duration |
| ---: | ---: | ---: |
| 0 | 57 | 1.69 s |
| 1 | 273 | 3.62 s |
| 2 | 121 | 8.43 s |
| 3 | 55 | 23.18 s |
| 4 | 15 | 33.08 s |
| 5 | 17 | 46.15 s |

This supports a fallback duration predictor from summaries when a future export
lacks block clocks. It does not make a summary a fixed token unit. Prefer the
actual interval whenever available.

Of 449 assistant messages, 281 contain some block clock and 280 have enough
clocks to compute an observed envelope. The other 168 have no block clocks.
Coverage varies by date and format: all 42 July assistant messages have clocks,
versus 95 of 193 in September. Missing clocks are not a random sample.

The assistant envelope's `created_at` is often later than its first block start:
median difference **24.33 seconds** across 281 messages. `updated_at` can occur
months after the response. Neither field should be substituted for request-send
or generation-completion time. Text and injected-prompt timestamps also need
their own semantics; many describe zero-length observations.

The 223 timed tool-result blocks have effectively identical start/stop clocks.
These locate result observation rather than an interval of tool execution.
Joining by `tool_use.id = tool_result.tool_use_id` gives 223 native
call-start-to-result-observation spans, median **1.30 seconds**, p90 **7.39
seconds**, p95 **11.18 seconds**. Three calls lack a matching result in their
message; unmatched results and fragments must also remain explicit.

The tool-use block's stop, when present, can describe completion of emitted
arguments. It is not a tool-completion label. A result without a native clock
does not acquire one from its enclosing assistant message.

In 53 of 450 thinking blocks with a following timed event, that event starts
before the thinking interval stops. Preserve source order and overlapping
intervals. Sorting everything by timestamps or summing every duration would
produce incorrect serial timelines.

### Coding usage has useful labels and reconciliation requirements

In the 9,633-request structural sample:

- 7,434 requests have explicit `output_tokens_details.thinking_tokens` evidence;
  5,003 are positive. An absent detail is unknown, distinct from an explicit zero.
- 20,649 usage-bearing events describe those requests. In 1,760 requests the
  recorded output counter changes across events. Summing events or keeping the
  first snapshot would give the wrong training label.
- Every sample request's projected output count matches its maximum native
  output snapshot. All 7,434 projected thinking counts with explicit details
  also match their maximum native detail. This validates this sample's labels,
  not a universal rule for future counter reset/iteration formats.
- Median output is 357 tokens per request; median processed input is 107,233.
  Median uncached base input is only two tokens. These measure very different
  things. The output p90 is 1,933.8 tokens; the broad distribution makes a fixed
  per-request or per-tool allowance a weak predictor.
- 659 requests have unreported effort. Missing effort must remain its own group.

Canonical processed Claude input includes base input, cache reads and cache
creation. Cache-detail fields are subdivisions, not additional input to add
again. [Prompt caching documentation](https://platform.claude.com/docs/en/build-with-claude/prompt-caching).

Two native timer spellings occur in sampled raw envelopes:
`thinkingDurationMs` and `thinking_duration_ms`. The existing numeric validation
projection recognizes the former. No positive explicit-thinking-token pair in
this sample uses the latter, so the second spelling is not yet an interchangeable
training label. Versioned capability extraction should retain both and validate
their scope separately.

`model_requests.requested_at` is the first retained usage-bearing response event,
not a verified request-send time. The existing Thinking dashboard deliberately
uses the gap after the latest linked incoming tool result. In the structural
sample that gap correlates with positive thinking tokens at 0.882, but it mixes
queueing, processing, generation and harness effects. The first-to-last retained
response-event span correlates only at 0.163. Dividing final output tokens by
that span would manufacture a misleading throughput measurement.

## What the calibration experiment establishes

The expanded timer population has Spearman correlation **0.883** between native
thinking timer and positive reported thinking tokens. Its token/timer ratio
has median 57.08 and p10–p90 24.19–95.48 tokens/second. This is a predictive
relationship between two recorded fields, not measured GPU throughput.

The following donor groups illustrate the need to retain model, effort, speed
and independent-family support. All are recorded standard speed; nearly all
timer requests have explicit `speed` provenance rather than a fallback.

| Recorded model and effort | Requests | Families | Median thinking tokens / recorded second | p10–p90 |
| --- | ---: | ---: | ---: | ---: |
| Opus 5.5 high | 37,940 | 473 | 50.94 | 22.53–91.79 |
| Opus 5.5 medium | 24,088 | 334 | 63.72 | 25.98–99.08 |
| Opus 5.5 xhigh | 3,344 | 9 | 59.20 | 30.03–91.71 |
| Sonnet 5.5 high | 3,108 | 5 | 53.72 | 24.38–106.61 |
| Fable 5.2 medium | 2,003 | 66 | 78.81 | 44.94–92.20 |
| Fable 5.2 high | 631 | 53 | 84.01 | 30.71–92.57 |

Thousands of requests from five families are weaker support for a new task than
hundreds of requests across many families. Do not equate request count with
independent sample size. Earlier models have substantial usage history but lack
comparable projected timer labels in this extraction; pool cautiously rather
than claiming every model has a learned thinking rate. Differences between
effort groups are confounded with workload, harness and date; this table does
not establish a causal effect of effort on generation speed.

### Held-out coding prediction

The pilot fits ridge regression to `log(1 + thinking_tokens)`, with model/effort/
speed group effects and, in the second model, log thinking duration. Training
weights give each linked conversation family equal total influence. Whole
families stay together across train/calibration/test; the target token count
is withheld from predictors. Intervals use a family-weighted quantile of
absolute calibration log residuals. This is an empirical diagnostic, not a
distribution-free guarantee for dependent data or chat transfer.

The stable family-hash split contains 42,101 training requests, 11,357 calibration
requests and **17,959 test requests from 188 families**. The nominal 80% interval
coverage should be checked rather than assumed. Exact results and the separate
forward-time evaluation are in the private calibration JSON; rounded headline
results are recorded below.

| Predictor | Typical multiplicative error | Predictions within 2× of reported thinking tokens |
| --- | ---: | ---: |
| Model, effort and speed | about 2.4× | about 41% |
| Same settings plus recorded thinking duration | about 1.4× | about 82% |

Adding true coding input/cache counts barely improved the random-family test
and made the forward test worse. Those inputs are unavailable in the target
chat export anyway. The initial transferable model should use duration and
settings, then test whether visible features improve it.

The timer model has 1.411× median factor error, 34.9% weighted absolute
percentage error (sum of absolute errors divided by sum of reported tokens),
and 82.0% of predictions within 2×. Its nominal 80% interval covers 77.3% of
the random-family test, with a multiplicative radius of 1.826×. The forward-time
test assigns whole families chronologically and removes validation rows before
their period boundary. It has 2,689 test requests from 200 families after November 10 at
22:31 UTC, but many model/effort/speed groups are new to its training set.
With duration and settings, this forward test has 1.369× median factor
error, 18.2% weighted absolute percentage error and 84.9% coverage of the nominal
80% interval. Its settings-only baseline has 3.541× median factor error.
Treat it as a short drift stress test, not a mature temporal benchmark.

The uncorrected timer model underpredicts aggregate thinking tokens by **25.1%**
on the random-family test and **15.6%** on the forward test. Its log-centered
prediction is useful for typical requests, but is not yet a calibrated expected
token count. Correct and revalidate that aggregate bias before using it for
expected-consumption totals. Factor errors here use the larger of the smoothed
prediction/observation ratio and its reciprocal, with one token added to each.

A separate structural-sample experiment predicts total output from visible
text size, displayed thinking size/count, tool-input size and settings. On
2,548 held-out requests from 38 families, median multiplicative error improves
from 2.43× for settings alone to 1.60× with those features; about 62% fall
within 2×. Its coding display features are not guaranteed to have chat-summary
semantics. It supports a fallback feature model, not a claim of chat accuracy.

The timer tests condition on positive reported thinking. They do not evaluate
detecting zero-thinking requests, requests with missing labels, or matching a
chat block interval to the coding timer. A production model needs a separate
zero/missing capability path. None of these tests provides observed chat token
ground truth, and no chat-specific accuracy percentage is asserted.

## Estimate the requested quantities separately

### Generated output tokens per turn

For an inferred model-response phase `j`, estimate:

```text
generated_output[j] = thinking[j] + nonthinking_output[j]
thinking[j] ~ duration/settings model when a comparable interval is available
nonthinking_output[j] ~ visible assistant text + serialized tool inputs + framing
generated_output[turn] = sum of its response phases
```

Train the nonthinking part against `output_tokens - explicit_thinking_tokens`
where both native labels exist. It includes tool arguments and protocol
overhead, as well as prose. Visible thinking summaries are predictors of hidden
work; counting them again as generated reasoning would double count the same
component. Do not use signature byte length as a token counter.

The first version can use a small hierarchical log-duration model with
model/effort/speed effects and a separate empirical visible-content model.
Evaluate a richer quantile model only when it beats these on held-out families.
Use known thinking mode and visibility regime as additional conditioning when
available. A fixed characters-per-token rule cannot handle omitted thinking,
large tool arguments and compressed summaries well enough by itself.

When clocks are missing, predict duration/thinking jointly from summary count,
summary length, displayed thinking length, output size, tool rounds and context
proxies. Use distinct models for raw-text-like, summary-only and omitted
representations. Zero visible thinking does not establish zero thinking tokens.
Retain a request-count distribution when phase boundaries are ambiguous.

### Processed input tokens per turn

Estimate this separately and give it wider uncertainty. For each inferred
request, build the selected ancestor history, new user input, injected context,
tool results available at that point, retained hidden reasoning and an uncertain
system/tool-definition contribution. Sum the requests' processed input, including
repeated history; do not count unique conversation text once and call that usage.

Compaction, context editing, attachments, hidden tools, memories and reasoning
retention affect the constructed prompt. Use model/version-specific policies
when known and explicit alternative scenarios otherwise. Learn coding context
growth and overhead as priors, without silently copying coding system-prompt
size into chat. The coding sample's 107k median input is not a chat default.

Cache read/write/base splits have less identification than total processed
input. Keep them as optional scenario estimates with their own provenance,
rather than exact-looking usage fields. Existing `tool_calls.result_tokens` can
be attributed or estimated by the ledger; it is not an independent measured
label for the number of tokens in a tool result.

### Tool execution and latency introduced by tools

Estimate call-start-to-result observation by **tool runtime, server, operation,
argument/result size, outcome and foreground/background mode**. Model/effort
is secondary for execution time; it mainly explains planning and the model
work before/after a call. Preserve explicit server durations separately from
transcript wall-clock spans and from argument-streaming time.

The observed distributions demonstrate the domain difference:

| Operation | Timed calls | Median | p90 |
| --- | ---: | ---: | ---: |
| Chat `web_search` | 106 | 1.12 s | 1.83 s |
| Coding `WebSearch` | 347 | 7.47 s | 14.23 s |
| Chat `web_fetch` | 25 | 0.98 s | 2.16 s |
| Coding `WebFetch` | 54 | 4.33 s | 13.98 s |
| Chat `str_replace` | 28 | 4.43 s | 11.61 s |
| Coding `Edit` | 350 | 0.010 s | 0.028 s |

These are different runtime/operation populations, not controlled comparisons
of identical work. Coding spans are mostly timestamp-derived; only eight of
the 10,400 timed coding calls have a reported duration. Start with native
chat spans where available and shrink sparse operations toward a compatible
runtime prior. Treat coding-only priors as fallbacks. Name/capitalization is a
format clue, not sufficient proof of equivalent implementations.

Calls without a result clock can still supply constraints. In a verified
synchronous same-stream sequence, if a subsequent assistant event depends on
the tool result, completion lies between call start and that event. The gap
contains tool execution plus resumption/model work. It supplies an upper bound
or interval-censored observation, not an exact execution label. A next unrelated
or parallel tool start does not establish that bound.

Show three different quantities when useful:

1. Estimated execution duration of this tool call.
2. Its blocking contribution to the turn's critical path.
3. The following model-response wait, with its own output-token estimate.

Parallel calls overlap; sum their compute/work durations separately from elapsed
waiting. Async agent launch acknowledgments are especially misleading: coding
`Agent` calls have a 15-ms median call/result span in this sample, often just an
acknowledgment rather than completion of delegated work. Link helper completion
and wait operations before estimating blocking latency. A counterfactual
"extra latency caused by using this tool" also changes model work and context;
label such an estimate as a scenario, not observed causal attribution.

### Total turn elapsed time

Represent the turn as a partially observed dependency graph: model phases emit
tool batches, completed results unblock later phases, and helpers have separate
streams. Anchor it to native clocks. Use the critical path and interval unions
to aggregate wall time; use sums to aggregate generated tokens and independent
compute. Keep approval pauses, user interaction, queueing and unexplained gaps
distinct instead of assigning every residual second to thinking.

One request can emit several tool calls. Some tool calls are server-managed;
some requests emit no tool. Neither `1 + tool_count` nor the number of thinking
blocks is an exact request count. Reconstruct clear boundaries first, then
retain alternative serial/parallel/hidden-continuation hypotheses for the rest.
Do not infer inaccessible inner thinking turns from a fixed summary ratio.

## An ongoing pipeline

```text
preserved source evidence and setting observations
    -> versioned format capabilities and identity reconciliation
    -> per-response / per-tool / per-turn numeric features
    -> donor calibration and masked-record evaluation
    -> partial-timeline inference and uncertainty propagation
    -> separate versioned estimates with explanations
```

### Evidence and features

Use the existing ingest and identity machinery. A generic provider adapter
should declare token semantics, request identity, counter scope, thinking
visibility, clock semantics and tool completion semantics. Shared estimation
policy consumes those declarations; OS-specific collection stays in the
existing native mechanisms.

Feature records need stable conversation/message/block/call identity; source
revision/content hash; selected branch and agent stream; parser/feature version;
field-presence bitmap; native clock values and provenance; observed model,
effort, thinking mode and speed with time scope; content-size measurements;
tool operation and execution mode; and dependency edges. Preserve unknown,
explicit zero, censored, interrupted and missing separately.

Deduplicate native/Conductor mirrors, replayed messages, shared-prefix forks
and streamed usage snapshots before fitting. Fit request settings at request
scope. Evaluate and resample by linked root family, repository and time where
those identities are available. The pilot links parents into families, but
does not establish independence across different families in the same repository.

The deployed catalog contains some populated chat result timestamps whose raw
block has no clock. The current `claudeAIToolBlockTime` and retained-time repair
code handle that distinction. The estimator must verify the evidence/parser
version and field presence, or derive features directly from native blocks,
before treating old projections as observed completion. No production repair
was performed for this survey.

### Calibration and refresh

Maintain separate calibrations for thinking tokens/duration, nonthinking
output, request-count/context growth, and tool execution. Store fitted
coefficients/distributions, donor scope, model/effort/runtime hierarchy,
independent-family support, date range, evaluation metrics, feature/schema
versions and clock eligibility rules in an immutable calibration artifact.

Prefer exact model + effort + mode + speed groups with hierarchical shrinkage
to the same model, then explicitly approved family priors. New model identities
start with a labeled fallback; no quiet renaming of neighboring models. A
runtime/operation hierarchy should govern tool execution independently.

Run bounded background feature extraction for changed source revisions.
Refresh calibration after enough new independent donor families arrive or on
a daily maintenance pass; avoid refitting on every read. Maintain per-partition
features and donor indexes so that an added turn does not rescan the entire
library. A changed old message or corrected setting invalidates affected turn
features and estimates, including descendants whose context changed. A changed
calibration invalidates estimates using it. Retire corrected donor rows before
retraining rather than accumulating both versions.

Use a recent window plus a pooled historical prior; compare 30/90-day choices
on forward tests rather than fixing a decay constant from intuition. Keep an
older calibration available when recent data is sparse. Promote a new version
only after quality checks, otherwise retain the last usable calibration and
explain its age.

Training can run offline first. A compact coefficient/quantile artifact and
deterministic Go inference would fit the service without a Python ML service
on the request path. Raw content already has a preservation home; the derived
store should hold numeric features and evidence references, not another full
transcript copy.

### Estimate storage and presentation

Keep estimated metrics out of `model_requests`' reported token columns and
out of measured tool durations. Suggested logical stores:

| Store | Grain and purpose |
| --- | --- |
| Estimation features | Source entity/revision; capabilities, numeric features and dependency edges |
| Calibration registry | Immutable fitted version, donor population and validation/support |
| Metric estimates | Entity + metric + feature/calibration/settings version; point estimate, interval/scenarios, support and evidence references |

The result should explain whether it uses a recorded interval, a native chat
tool prior, a coding prior, an inferred request count or an unreported setting.
Provide a central estimate and a predictive range, plus separate transfer and
missing-context sensitivity. A confidence interval for the mean token rate is
not a prediction interval for the next turn. Round output to the uncertainty;
do not present inferred token counts as exact integers with apparent precision.

Distinguish **typical** from **expected** usage and latency. Exponentiating a
log-regression prediction gives a log-centered estimate, not the arithmetic
expectation needed for expected totals. Estimate residual distributions and
validate a retransformation correction or use direct conditional-mean models
for that purpose. Report mean and median separately for heavy-tailed tool waits.
Expected token totals add across requests; prediction ranges still require
their shared uncertainty. Monitor signed aggregate bias as well as per-request
factor error before presenting a model as an expected-consumption estimator.

When the other thread supplies model/effort, attach it as versioned setting
evidence with conversation/turn scope, observation time and selected-versus-
execution provenance. Recompute relevant estimates without reparsing preserved
text. Until then, offer explicit conditional estimates or a labeled mixture
over plausible settings. Coding-fleet prevalence is not the chat model mixture,
and timing-derived model guesses must not be fed back as independent labels.

For turn/conversation totals, propagate distributions through the graph rather
than summing p90 values. Sample shared model-rate/runtime effects jointly within
a turn, so uncertainty about every request's model is not incorrectly averaged
away. Keep observed and estimated contributions separately inspectable.

Any Query Table capability needed for eventual presentation must use a published
upstream release under the repository policy. The estimator itself does not
require a frontend or local Query Table patch.

## Validation and implementation sequence

1. **Build the feature contract and offline benchmark.** Reuse native request
   reconciliation. Extend coverage tests for summaries, counter snapshots,
   missing clocks, branches, helpers, conflicting settings and explicit zero.
   Freeze private numeric study snapshots for reproducibility.
2. **Mask coding transcripts to match actual export regimes.** Collapse coding
   responses into turns; remove usage, request boundaries and internal timers;
   retain only clocks/summary visibility that a selected chat format supplies.
   Test reconstruction, output tokens, context and tool duration independently.
   Test artificial timestamp removal against observed missingness patterns,
   rather than uniformly deleting timestamps. Coding summaries are not a
   perfect simulation of chat summaries; retain that regime distinction.
3. **Validate tools on native chat clocks.** Hold out entire chat conversations
   or date blocks among the 223 native pairs. Test compatible coding fallbacks
   against those withheld durations. Publish support counts by operation;
   rare tools use broader priors rather than unstable tail percentiles.
4. **Ship generated-output and tool-execution estimates first.** Preserve direct
   intervals, attach model/effort when available, and expose basis/support.
   Enable input/context and missing-request scenarios separately as their
   benchmarks become useful.
5. **Add bounded refresh and monitoring.** Track absolute error, aggregate bias,
   factor error and interval coverage by runtime, visibility, model/effort,
   zero-thinking status, tool family and context/length bands. Use family-held-
   out and forward-time tests. Bootstrap whole families for uncertainty in
   validation metrics; do not treat thousands of same-session requests as
   independent confirmations. Detect drift and fallback usage explicitly.

These stages can produce estimates without waiting for inaccessible chat usage.
The principal near-term dependency is correctly scoped model/effort evidence,
not confirmation that chat and coding internals are identical.

## Existing implementation points

- [`adapter_claude_ai.go`](../internal/archive/adapter_claude_ai.go): original
  block fields, thinking summary selection, tool clock semantics and retained
  timestamp repair.
- [`adapters.go`](../internal/archive/adapters.go): `jsonlAdapter.claude`, native
  usage envelopes, event identities and per-event execution settings.
- [`adapter_conductor.go`](../internal/archive/adapter_conductor.go): SDK events,
  stream/delegation handling and `conductorSessionEffort`.
- [`token_usage.go`](../internal/archive/token_usage.go) and
  [`tool_ledger.go`](../internal/archive/tool_ledger.go): token normalization,
  request reconciliation, tool linkage and duration provenance. Reuse their
  identities; do not build a competing accounting path.
- [`thinking_table.go`](../internal/archive/thinking_table.go) and
  [`thinking_validation.go`](../internal/archive/thinking_validation.go): stored
  gap/timer evidence and dirty-conversation maintenance, with the clock
  limitations documented in [Thinking dashboards](thinking-dashboards.md).
- [`claude_chat_settings.go`](../internal/archive/claude_chat_settings.go):
  allowlisted setting observations; extend association through a separately
  reviewed time-scope contract rather than assuming historical execution.

Private survey artifacts are `.context/claude-estimation-survey.py`,
`claude-estimation-analysis.py` and `claude-timer-calibration.py`, with matching
JSON summaries and numeric feature extracts. They require Python with NumPy
and SciPy for the calibration experiments. The first survey reads the original
ZIP identified by retained evidence; the expanded timer extraction reads the
existing numeric projection. Retain these private artifacts with this workspace
when reproducing the study; source transcripts are not copied into the report.
